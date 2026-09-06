package channels

import (
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	"github.com/tokenrouter/tokenrouter/internal/platform/textutil"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strings"
)

// channelSortColumns is the reference sort whitelist for channel lists; any
// other column falls back to the default ordering.
var channelSortColumns = map[string]string{
	"id":            "id",
	"name":          "name",
	"priority":      "priority",
	"balance":       "balance",
	"response_time": "response_time",
	"test_time":     "test_time",
}

// ChannelSortOptions carries the reference channel-list sort contract.
type ChannelSortOptions struct {
	SortBy    string
	SortOrder string
	IDSort    bool
}

// NewChannelSortOptions normalizes sort parameters against the whitelist: an
// unknown column clears both fields (so Apply uses the default ordering).
func NewChannelSortOptions(sortBy, sortOrder string, idSort bool) ChannelSortOptions {
	normalizedSortBy := strings.ToLower(strings.TrimSpace(sortBy))
	normalizedSortOrder := strings.ToLower(strings.TrimSpace(sortOrder))
	if _, ok := channelSortColumns[normalizedSortBy]; !ok {
		normalizedSortBy = ""
		normalizedSortOrder = ""
	} else if normalizedSortOrder != "asc" {
		normalizedSortOrder = "desc"
	}
	return ChannelSortOptions{SortBy: normalizedSortBy, SortOrder: normalizedSortOrder, IDSort: idSort}
}

// Apply orders the query: whitelisted column first, then id desc when the
// caller asked for id sort, then the reference default of priority desc.
func (options ChannelSortOptions) Apply(query *gorm.DB) *gorm.DB {
	if columnName, ok := channelSortColumns[options.SortBy]; ok {
		return query.Order(clause.OrderByColumn{
			Column: clause.Column{Name: columnName},
			Desc:   options.SortOrder != "asc",
		})
	}
	if options.IDSort {
		return query.Order(clause.OrderByColumn{
			Column: clause.Column{Name: "id"},
			Desc:   true,
		})
	}
	return query.Order(clause.OrderByColumn{
		Column: clause.Column{Name: "priority"},
		Desc:   true,
	})
}

// NormalizeChannelGroupFilter maps the special group filter values ("all",
// "null", blank) to no filter.
func NormalizeChannelGroupFilter(group string) string {
	group = strings.TrimSpace(group)
	if group == "" || strings.EqualFold(group, "all") || strings.EqualFold(group, "null") {
		return ""
	}
	return group
}

// applyChannelGroupFilter adds a comma-list membership test on the group
// column with LIKE-escape sanitization (reference contract).
func applyChannelGroupFilter(query *gorm.DB, group string) *gorm.DB {
	group = NormalizeChannelGroupFilter(group)
	if group == "" {
		return query
	}
	escaped := strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(group)
	if model.UsingMySQL() {
		return query.Where("CONCAT(',', `group`, ',') LIKE ? ESCAPE '!'", "%,"+escaped+",%")
	}
	return query.Where("(',' || \"group\" || ',') LIKE ? ESCAPE '!'", "%,"+escaped+",%")
}

// channelSearchWhere builds the channel search condition. Exact credential
// matching is available only to callers that already hold the sensitive-write
// permission; ordinary readers must not get a credential-guessing oracle.
func channelSearchWhere(keyword, modelKeyword string, includeKey bool) (string, []any) {
	if includeKey {
		where := "(id = ? OR name LIKE ? OR key = ? OR base_url LIKE ?) AND models LIKE ?"
		return where, []any{
			textutil.Str2Int(keyword), "%" + keyword + "%", keyword, "%" + keyword + "%",
			"%" + modelKeyword + "%",
		}
	}
	where := "(id = ? OR name LIKE ? OR base_url LIKE ?) AND models LIKE ?"
	return where, []any{
		textutil.Str2Int(keyword), "%" + keyword + "%", "%" + keyword + "%",
		"%" + modelKeyword + "%",
	}
}

// SearchChannels searches channels by keyword (id/name/exact key/base URL) and
// model substring, filtered by group, with the sort whitelist applied. The key
// column is always omitted from results.
func SearchChannels(keyword, group, modelKeyword string, sortOptions ChannelSortOptions, includeKey bool) ([]*model.Channel, error) {
	where, args := channelSearchWhere(keyword, modelKeyword, includeKey)
	query := model.DB.Model(&model.Channel{}).Omit("key")
	query = applyChannelGroupFilter(query.Where(where, args...), group)
	var channels []*model.Channel
	err := sortOptions.Apply(query).Find(&channels).Error
	return channels, err
}

// SearchChannelTags returns the distinct tags of channels matching the same
// search condition, ordered by priority desc (or id desc with idSort).
func SearchChannelTags(keyword, group, modelKeyword string, idSort, includeKey bool) ([]string, error) {
	where, args := channelSearchWhere(keyword, modelKeyword, includeKey)
	order := clause.OrderByColumn{Column: clause.Column{Name: "priority"}, Desc: true}
	if idSort {
		order = clause.OrderByColumn{Column: clause.Column{Name: "id"}, Desc: true}
	}
	subQuery := applyChannelGroupFilter(
		model.DB.Model(&model.Channel{}).Where(where, args...), group).
		Select("tag").
		Where("tag != ''").
		Order(order)

	var tags []string
	err := model.DB.Table("(?) as sub", subQuery).
		Select("DISTINCT tag").
		Find(&tags).Error
	if err != nil {
		return nil, err
	}
	return tags, nil
}

// GetEnabledChannelModels returns the union of model names declared on
// status-enabled channels (the reference enabled-model catalog).
func GetEnabledChannelModels() ([]string, error) {
	var channels []model.Channel
	if err := model.DB.Where("status = ?", channelcatalog.ChannelStatusEnabled).Find(&channels).Error; err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	names := make([]string, 0)
	for _, ch := range channels {
		for _, m := range strings.Split(ch.Models, ",") {
			if m = strings.TrimSpace(m); m != "" && !seen[m] {
				seen[m] = true
				names = append(names, m)
			}
		}
	}
	return names, nil
}

// RetryTimes returns the request-independent retry policy snapshot. An
// explicit zero is terminal; deployment environment precedence is resolved
// once by the settings loader rather than re-read with fallback semantics.
func RetryTimes() int {
	return setting.GetChannelReliabilitySetting().RetryTimes
}
