package tasks

import (
	"bytes"
	"context"
	"errors"
	"github.com/gin-gonic/gin"
	channelssvc "github.com/tokenrouter/tokenrouter/internal/channels"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/middleware"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/requestctx"
	geminiVeo "github.com/tokenrouter/tokenrouter/internal/relay/providers/task/gemini"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// VeoProviderTask is the bounded provider-neutral result consumed by the one
// durable Veo state machine. A provider may return either a URL or an inline
// video, never both.
type VeoProviderTask struct {
	ProviderTaskID string
	Status         geminiVeo.TaskStatus
	ErrorCode      string
	ErrorMessage   string
	ResultURL      string
	InlineMIMEType string
	InlineBase64   string
	InlineBytes    int64
}

// VeoTaskProviderDescriptor adapts one selected Google channel family to the
// common durable ledger/recovery machinery. All callbacks must be deterministic
// from the frozen routing snapshot and encrypted credential.
type VeoTaskProviderDescriptor struct {
	ChannelType             channelcatalog.ChannelType
	Platform                string
	Family                  string
	MaxCredentialBytes      int
	MaxProviderTaskIDBytes  int
	CaptureCredential       func(*model.Channel) (string, error)
	PrepareRoutingSnapshot  func(*model.Channel, string) (string, error)
	ValidateRoutingSnapshot func(string, string) error
	ValidateProviderTaskID  func(string, string, string) error
	ValidatePrepared        func(*geminiVeo.PreparedRequest) error
	Submit                  func(context.Context, string, string, *geminiVeo.PreparedRequest) (*VeoProviderTask, []byte, error)
	Fetch                   func(context.Context, string, string, string, string) (*VeoProviderTask, []byte, error)
	Content                 func(context.Context, string, string, VeoProviderTask) (*http.Response, error)
	SubmitWasDispatched     func(error) bool
}

// VeoTaskChannelSelection is the single channel decision shared by Gemini API
// and Vertex Veo. Handlers must use this channel and must not select again.
type VeoTaskChannelSelection struct {
	RawRequest  []byte
	ContentType string
	OriginModel string
	UsingGroup  string
	Channel     *model.Channel
}

// VeoTaskChannelHandler owns the provider-specific durable lifecycle after a
// channel has been selected by model and authorized group.
type VeoTaskChannelHandler func(*gin.Context, VeoTaskChannelSelection)

var veoTaskHandlers = struct {
	sync.RWMutex
	byChannelType map[channelcatalog.ChannelType]VeoTaskChannelHandler
}{byChannelType: make(map[channelcatalog.ChannelType]VeoTaskChannelHandler)}

var veoTaskProviders = struct {
	sync.RWMutex
	byChannelType map[channelcatalog.ChannelType]VeoTaskProviderDescriptor
	byPlatform    map[string]VeoTaskProviderDescriptor
	byFamily      map[string]VeoTaskProviderDescriptor
}{
	byChannelType: make(map[channelcatalog.ChannelType]VeoTaskProviderDescriptor),
	byPlatform:    make(map[string]VeoTaskProviderDescriptor),
	byFamily:      make(map[string]VeoTaskProviderDescriptor),
}

// RegisterVeoTaskProvider installs one provider descriptor and wires it to the
// shared channel-first dispatcher. Platform must be the selected channel type.
func RegisterVeoTaskProvider(descriptor VeoTaskProviderDescriptor) {
	wantPlatform := strconv.Itoa(int(descriptor.ChannelType))
	if descriptor.ChannelType <= 0 || descriptor.Platform != wantPlatform || descriptor.Family == "" ||
		len(descriptor.Family) > 64 || descriptor.MaxCredentialBytes <= 0 ||
		descriptor.MaxCredentialBytes > 256<<10 || descriptor.MaxProviderTaskIDBytes <= 0 ||
		descriptor.MaxProviderTaskIDBytes > 4096 || descriptor.PrepareRoutingSnapshot == nil ||
		descriptor.CaptureCredential == nil || descriptor.ValidateRoutingSnapshot == nil ||
		descriptor.ValidateProviderTaskID == nil || descriptor.ValidatePrepared == nil ||
		descriptor.Submit == nil || descriptor.Fetch == nil ||
		descriptor.Content == nil || descriptor.SubmitWasDispatched == nil {
		panic("invalid Veo task provider descriptor")
	}
	veoTaskProviders.Lock()
	if _, exists := veoTaskProviders.byChannelType[descriptor.ChannelType]; exists {
		veoTaskProviders.Unlock()
		panic("duplicate Veo task provider channel type")
	}
	if _, exists := veoTaskProviders.byPlatform[descriptor.Platform]; exists {
		veoTaskProviders.Unlock()
		panic("duplicate Veo task provider platform")
	}
	if _, exists := veoTaskProviders.byFamily[descriptor.Family]; exists {
		veoTaskProviders.Unlock()
		panic("duplicate Veo task provider family")
	}
	veoTaskProviders.byChannelType[descriptor.ChannelType] = descriptor
	veoTaskProviders.byPlatform[descriptor.Platform] = descriptor
	veoTaskProviders.byFamily[descriptor.Family] = descriptor
	veoTaskProviders.Unlock()
	RegisterVeoTaskChannelHandler(descriptor.ChannelType, relayVeoTaskWithProvider)
}

func veoTaskProviderByFamily(family string) (VeoTaskProviderDescriptor, bool) {
	veoTaskProviders.RLock()
	defer veoTaskProviders.RUnlock()
	descriptor, ok := veoTaskProviders.byFamily[family]
	return descriptor, ok
}

func veoTaskProviderByChannelType(channelType channelcatalog.ChannelType) (VeoTaskProviderDescriptor, bool) {
	veoTaskProviders.RLock()
	defer veoTaskProviders.RUnlock()
	descriptor, ok := veoTaskProviders.byChannelType[channelType]
	return descriptor, ok
}

func veoTaskProviderByPlatform(platform string) (VeoTaskProviderDescriptor, bool) {
	veoTaskProviders.RLock()
	defer veoTaskProviders.RUnlock()
	descriptor, ok := veoTaskProviders.byPlatform[platform]
	return descriptor, ok
}

// RegisterVeoTaskChannelHandler adds a channel-specific Veo lifecycle without
// changing the shared model detector. Duplicate registration fails closed.
func RegisterVeoTaskChannelHandler(channelType channelcatalog.ChannelType, handler VeoTaskChannelHandler) {
	if channelType <= 0 || handler == nil {
		panic("invalid Veo task channel handler")
	}
	veoTaskHandlers.Lock()
	defer veoTaskHandlers.Unlock()
	if _, exists := veoTaskHandlers.byChannelType[channelType]; exists {
		panic("duplicate Veo task channel handler")
	}
	veoTaskHandlers.byChannelType[channelType] = handler
}

func veoTaskHandler(channelType channelcatalog.ChannelType) VeoTaskChannelHandler {
	veoTaskHandlers.RLock()
	defer veoTaskHandlers.RUnlock()
	return veoTaskHandlers.byChannelType[channelType]
}

// relayVeoTask recognizes the shared four-model family, chooses one eligible
// channel exactly once, then dispatches by that selected channel's type.
func relayVeoTask(c *gin.Context, raw []byte, contentType string) bool {
	originModel := declaredVeoTaskModel(raw, contentType)
	if !geminiVeo.IsModel(originModel) {
		return false
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(raw))
	if requestctx.GetUserId(c) <= 0 || middleware.GetRelayToken(c) == nil {
		writeVideoTaskError(c, http.StatusInternalServerError, "auth_context_missing", "relay token context is missing", nil)
		return true
	}
	groups := middleware.GetTokenGroups(c)
	if len(groups) == 0 {
		writeVideoTaskError(c, http.StatusForbidden, "group_not_allowed", "token group is unavailable", nil)
		return true
	}
	if !middleware.RelayModelAllowed(c, originModel) {
		writeVideoTaskError(c, http.StatusForbidden, "model_not_allowed", "token is not allowed to access model "+originModel, nil)
		return true
	}
	channel, usingGroup, err := selectVeoTaskChannel(groups, originModel)
	if err != nil {
		writeVideoTaskError(c, http.StatusServiceUnavailable, "channel_not_found", "no Veo channel is available", nil)
		return true
	}
	handler := veoTaskHandler(channelcatalog.ChannelType(channel.Type))
	if handler == nil {
		writeVideoTaskError(c, http.StatusServiceUnavailable, "channel_not_supported", "selected Veo channel is unavailable", nil)
		return true
	}
	handler(c, VeoTaskChannelSelection{
		RawRequest: append([]byte(nil), raw...), ContentType: contentType,
		OriginModel: originModel, UsingGroup: usingGroup, Channel: channel,
	})
	return true
}

func declaredVeoTaskModel(raw []byte, contentType string) string {
	return geminiVeo.DeclaredModel(raw, contentType)
}

func selectVeoTaskChannel(groups []string, modelName string) (*model.Channel, string, error) {
	ignored := make(map[int]struct{})
	for attempt := 0; attempt < 128; attempt++ {
		channel, group, err := channelssvc.GetRandomSatisfiedChannelFromGroups(groups, modelName, ignored, nil)
		if err != nil {
			return nil, "", err
		}
		if channel == nil || channel.Id <= 0 {
			return nil, "", errors.New("invalid Veo channel selection")
		}
		if veoTaskHandler(channelcatalog.ChannelType(channel.Type)) != nil {
			return channel, group, nil
		}
		ignored[channel.Id] = struct{}{}
	}
	return nil, "", channelssvc.ErrChannelNotFound
}

func normalizedVeoContentType(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "application/json"
	}
	return value
}
