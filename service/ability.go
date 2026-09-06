package service

import (
	"context"
	"errors"
	"math/rand"
	"sort"
	"sync"

	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
)

// ErrChannelNotFound is returned when no channel satisfies a selection.
var ErrChannelNotFound = errors.New("no available channel")

var (
	abilityMu    sync.RWMutex
	abilityCache = map[string][]*model.Ability{} // key: group:model
)

// InitAbilityCache loads all enabled abilities into memory.
func InitAbilityCache() error {
	return InitAbilityCacheContext(context.Background())
}

// InitAbilityCacheContext loads all enabled abilities into memory while
// allowing a lost scheduler lease or process shutdown to cancel the database
// read and the in-memory rebuild. The previous cache remains published unless
// the complete replacement was built successfully.
func InitAbilityCacheContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("ability-cache context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var abilities []model.Ability
	if err := model.DB.WithContext(ctx).Where("enabled = ?", true).Find(&abilities).Error; err != nil {
		return err
	}
	replacement := make(map[string][]*model.Ability, len(abilities))
	for i := range abilities {
		if err := ctx.Err(); err != nil {
			return err
		}
		a := &abilities[i]
		key := abilityKey(a.Group, a.Model)
		replacement[key] = append(replacement[key], a)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	abilityMu.Lock()
	abilityCache = replacement
	abilityMu.Unlock()
	return nil
}

// SyncAbilityCache reloads abilities (hot reload).
func SyncAbilityCache() error {
	return InitAbilityCache()
}

// SyncAbilityCacheContext is SyncAbilityCache's cancellable form.
func SyncAbilityCacheContext(ctx context.Context) error {
	return InitAbilityCacheContext(ctx)
}

func abilityKey(group, modelName string) string {
	return group + ":" + modelName
}

// GetGroupModels returns the distinct models available in a group.
func GetGroupModels(group string) map[string]bool {
	abilityMu.RLock()
	defer abilityMu.RUnlock()
	models := make(map[string]bool)
	for key := range abilityCache {
		// key format group:model — strip group prefix
		if len(key) > len(group) && key[:len(group)+1] == group+":" {
			models[key[len(group)+1:]] = true
		}
	}
	return models
}

// selectableChannel pairs an ability with its channel record.
type selectableChannel struct {
	channel  *model.Channel
	priority int64
	weight   uint
}

// GetRandomSatisfiedChannel selects a channel for (group, model), excluding
// channels in ignoreChannel. Selection is priority-first, then weighted-random
// among channels sharing the highest priority. r may be nil (uses a default
// source); tests pass a seeded rand for determinism.
func GetRandomSatisfiedChannel(group, modelName string, ignoreChannel map[int]struct{}, r *rand.Rand) (*model.Channel, error) {
	candidates := collectCandidates(group, modelName, ignoreChannel)
	if len(candidates) == 0 {
		return nil, ErrChannelNotFound
	}

	// Priority-first selection.
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].priority > candidates[j].priority
	})
	best := candidates[0].priority
	var pool []*selectableChannel
	for _, c := range candidates {
		if c.priority == best {
			pool = append(pool, c)
		}
	}

	if len(pool) == 1 {
		return pool[0].channel, nil
	}

	// Weighted random among the highest-priority pool.
	weights := make([]uint, len(pool))
	for i, c := range pool {
		w := c.weight
		if w == 0 {
			w = constant.DefaultChannelWeight
		}
		weights[i] = w
	}
	idx := weightedRandomIndex(r, weights)
	return pool[idx].channel, nil
}

// GetRandomSatisfiedChannelFromGroups selects from the first authorized group
// (in caller-provided order) that has an eligible channel. It never injects a
// default-group fallback and returns the actual group used for pricing/logging.
func GetRandomSatisfiedChannelFromGroups(groups []string, modelName string, ignoreChannel map[int]struct{}, r *rand.Rand) (*model.Channel, string, error) {
	for _, group := range groups {
		if group == "" || group == GroupAuto {
			continue
		}
		channel, err := GetRandomSatisfiedChannel(group, modelName, ignoreChannel, r)
		if err == nil {
			return channel, group, nil
		}
		if !errors.Is(err, ErrChannelNotFound) {
			return nil, "", err
		}
	}
	return nil, "", ErrChannelNotFound
}

func collectCandidates(group, modelName string, ignore map[int]struct{}) []*selectableChannel {
	abilityMu.RLock()
	abilities := abilityCache[abilityKey(group, modelName)]
	abilityMu.RUnlock()
	if len(abilities) == 0 {
		return nil
	}

	channelIDs := make([]int, 0, len(abilities))
	priorityByChannel := make(map[int]int64, len(abilities))
	for _, a := range abilities {
		if _, skip := ignore[a.ChannelId]; skip {
			continue
		}
		if _, seen := priorityByChannel[a.ChannelId]; !seen {
			channelIDs = append(channelIDs, a.ChannelId)
		}
		if a.Priority != nil {
			priorityByChannel[a.ChannelId] = *a.Priority
		}
	}

	if len(channelIDs) == 0 {
		return nil
	}

	var channels []model.Channel
	if err := model.DB.Where("id IN ? AND status = ?", channelIDs, constant.ChannelStatusEnabled).Find(&channels).Error; err != nil {
		return nil
	}

	weightByChannel := make(map[int]uint, len(abilities))
	for _, a := range abilities {
		if _, skip := ignore[a.ChannelId]; skip {
			continue
		}
		weightByChannel[a.ChannelId] = a.Weight
	}

	out := make([]*selectableChannel, 0, len(channels))
	for i := range channels {
		ch := &channels[i]
		prio := priorityByChannel[ch.Id]
		if ch.Priority != nil {
			prio = *ch.Priority
		}
		// The ability weight is the routing weight; channel.Weight is a
		// fallback for legacy single-channel configuration.
		w := weightByChannel[ch.Id]
		if w == 0 && ch.Weight != nil {
			w = *ch.Weight
		}
		if w == 0 {
			w = constant.DefaultChannelWeight
		}
		out = append(out, &selectableChannel{channel: ch, priority: prio, weight: w})
	}
	return out
}

// weightedRandomIndex picks an index weighted by weights using r (or a default
// source when r is nil).
func weightedRandomIndex(r *rand.Rand, weights []uint) int {
	if len(weights) == 0 {
		return -1
	}

	// A database row may predate current input validation or have been written
	// outside the dashboard. Summing arbitrary uint weights directly can wrap;
	// converting that wrapped value to int can then make rand.Intn panic. Scale
	// every weight by the same power of two until the total fits Int63n. Rounding
	// non-zero weights upward keeps every configured channel selectable while
	// preserving the relative distribution as closely as integer arithmetic
	// permits.
	scaled := make([]uint64, len(weights))
	for i, weight := range weights {
		scaled[i] = uint64(weight)
	}
	const maxWeightedRandomTotal = uint64(^uint64(0) >> 1)
	var total uint64
	for {
		total = 0
		fits := true
		for _, weight := range scaled {
			if weight > maxWeightedRandomTotal-total {
				fits = false
				break
			}
			total += weight
		}
		if fits {
			break
		}
		for i, weight := range scaled {
			if weight > 1 {
				scaled[i] = weight/2 + weight%2
			}
		}
	}
	if total == 0 {
		if r != nil {
			return r.Intn(len(scaled))
		}
		return rand.Intn(len(scaled))
	}
	var roll uint64
	if r != nil {
		roll = uint64(r.Int63n(int64(total)))
	} else {
		roll = uint64(rand.Int63n(int64(total)))
	}
	var acc uint64
	for i, weight := range scaled {
		acc += weight
		if roll < acc {
			return i
		}
	}
	return len(scaled) - 1
}

// GetSatisfiedChannelWithPreferred selects an eligible channel for a request.
// A preferred channel is honored only when it has an enabled ability for the
// requested group/model and has not already failed in this retry sequence.
func GetSatisfiedChannelWithPreferred(group, modelName string, preferredChannelID int, ignore map[int]struct{}, r *rand.Rand) (*model.Channel, bool, error) {
	if preferredChannelID > 0 {
		candidates := collectCandidates(group, modelName, ignore)
		for _, candidate := range candidates {
			if candidate.channel.Id == preferredChannelID {
				return candidate.channel, true, nil
			}
		}
	}
	channel, err := GetRandomSatisfiedChannel(group, modelName, ignore, r)
	return channel, false, err
}

// GetChannelByID loads a channel by id.
func GetChannelByID(id int) (*model.Channel, error) {
	var channel model.Channel
	if err := model.DB.First(&channel, id).Error; err != nil {
		return nil, err
	}
	return &channel, nil
}

// GetChannelName returns the display name for a channel id (empty for 0).
func GetChannelName(id int) string {
	if id == 0 {
		return ""
	}
	ch, err := GetChannelByID(id)
	if err != nil {
		return ""
	}
	if ch.Name != "" {
		return ch.Name
	}
	return constant.ChannelTypeName(constant.ChannelType(ch.Type))
}
