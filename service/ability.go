package service

import (
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
	var abilities []model.Ability
	if err := model.DB.Where("enabled = ?", true).Find(&abilities).Error; err != nil {
		return err
	}
	abilityMu.Lock()
	defer abilityMu.Unlock()
	abilityCache = make(map[string][]*model.Ability, len(abilities))
	for i := range abilities {
		a := &abilities[i]
		key := abilityKey(a.Group, a.Model)
		abilityCache[key] = append(abilityCache[key], a)
	}
	return nil
}

// SyncAbilityCache reloads abilities (hot reload).
func SyncAbilityCache() error {
	return InitAbilityCache()
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
		// Fall back to the default group when the specific group has nothing.
		if group != GroupDefault {
			candidates = collectCandidates(GroupDefault, modelName, ignoreChannel)
		}
	}
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
	var total uint
	for _, w := range weights {
		total += w
	}
	if total == 0 {
		if r != nil {
			return r.Intn(len(weights))
		}
		return rand.Intn(len(weights))
	}
	var roll int
	if r != nil {
		roll = r.Intn(int(total))
	} else {
		roll = rand.Intn(int(total))
	}
	acc := 0
	for i, w := range weights {
		acc += int(w)
		if roll < acc {
			return i
		}
	}
	return len(weights) - 1
}

// GetSatisfiedChannelWithAffinity selects a channel, preferring the pinned
// (affinity) channel for a user+model when it is still enabled and not ignored.
func GetSatisfiedChannelWithAffinity(group, modelName string, userId int, ignore map[int]struct{}, r *rand.Rand) (*model.Channel, error) {
	if affinity := GetAffinityChannel(userId, modelName); affinity > 0 {
		if _, skip := ignore[affinity]; !skip {
			if ch, err := GetChannelByID(affinity); err == nil && ch.Status == constant.ChannelStatusEnabled {
				return ch, nil
			}
		}
	}
	return GetRandomSatisfiedChannel(group, modelName, ignore, r)
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
