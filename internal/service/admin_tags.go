package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"github.com/Foodan-Dev/danshi-backend/internal/apierr"
	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/pkg/pagination"
	"github.com/Foodan-Dev/danshi-backend/internal/pkg/ptime"
	"github.com/Foodan-Dev/danshi-backend/internal/repository"
)

// AdminTagListInput 是标签管理列表的筛选与游标。
type AdminTagListInput struct {
	Name       string
	Moderation string
	IsDeleted  *bool
	Pagination pagination.CursorRequest
}

// AdminTagView 是管理端标签当前状态。
type AdminTagView struct {
	ID         uint64                 `json:"id"`
	Name       string                 `json:"name"`
	Moderation model.ModerationStatus `json:"moderation"`
	IsDeleted  bool                   `json:"is_deleted"`
	DeletedAt  *ptime.Time            `json:"deleted_at"`
	CreatedAt  ptime.Time             `json:"created_at"`
	UpdatedAt  ptime.Time             `json:"updated_at"`
}

// AdminTagList 是标签管理游标页。
type AdminTagList struct {
	Tags       []AdminTagView        `json:"tags"`
	Pagination pagination.CursorMeta `json:"pagination"`
}

// RenameAdminTagInput 是标签重命名请求。
type RenameAdminTagInput struct {
	Name string
}

// MergeAdminTagInput 是标签合并请求。
type MergeAdminTagInput struct {
	TargetTagID uint64
}

// AdminTagMergeResult 是标签合并后的源、目标与受影响帖子数。
type AdminTagMergeResult struct {
	Source            AdminTagView `json:"source"`
	Target            AdminTagView `json:"target"`
	AffectedPostCount int          `json:"affected_post_count"`
}

// AdminHotTagView 是一个实时热门标签统计项。
type AdminHotTagView struct {
	ID        uint64 `json:"id"`
	Name      string `json:"name"`
	PostCount int64  `json:"post_count"`
}

// AdminHotTagList 是实时热门标签 TopN。
type AdminHotTagList struct {
	Tags []AdminHotTagView `json:"tags"`
}

type tagAffectedPost struct {
	post   model.Post
	latest model.PostHistory
}

// Tags 返回话题标签管理列表。
func (s *AdminService) Tags(ctx context.Context, input AdminTagListInput) (*AdminTagList, error) {
	params, err := s.tagCursor.DecodeRequest(input.Pagination)
	if err != nil {
		return nil, err
	}
	filter, err := adminTagFilter(input)
	if err != nil {
		return nil, err
	}
	rows, hasMore, err := s.admin.FindTagCursorPage(ctx, filter, params)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	items := make([]AdminTagView, 0, len(rows))
	for index := range rows {
		items = append(items, adminTagView(&rows[index]))
	}
	meta := pagination.CursorMeta{Limit: params.Limit, HasMore: hasMore}
	if hasMore {
		last := rows[len(rows)-1]
		token, encodeErr := s.tagCursor.Encode(pagination.Cursor{CreatedAt: last.CreatedAt, ID: last.ID})
		if encodeErr != nil {
			return nil, apierr.Internal(encodeErr)
		}
		meta.NextCursor = &token
	}
	return &AdminTagList{Tags: items, Pagination: meta}, nil
}

// RenameTag 规范化新名称并为所有关联帖子追加一致的新快照版本。
func (s *AdminService) RenameTag(
	ctx context.Context,
	tagID uint64,
	actorID uint64,
	input RenameAdminTagInput,
) (*AdminTagView, error) {
	name, err := normalizeAdminTagName(input.Name)
	if err != nil {
		return nil, err
	}
	tags, err := s.admin.LockTagsByIDs(ctx, []uint64{tagID})
	if err != nil {
		return nil, apierr.Internal(err)
	}
	if len(tags) != 1 {
		return nil, apierr.NotFound(apierr.BizTagNotFound, "标签")
	}
	if tags[0].Name == name {
		view := adminTagView(&tags[0])
		return &view, nil
	}
	affected, err := s.prepareTagAffectedPosts(ctx, tagID)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if err := s.admin.RenameTag(ctx, tagID, name, now); err != nil {
		if repository.IsSQLState(err, "23505") {
			return nil, apierr.Conflict(
				apierr.BizTagNameConflict, "同名标签已存在，请改用标签合并端点",
			)
		}
		return nil, repository.ToAPIError(err, apierr.BizTagNotFound, "标签")
	}
	if err := s.appendTagChangeHistories(
		ctx, affected, actorID, now, fmt.Sprintf("话题标签 #%d 重命名", tagID),
	); err != nil {
		return nil, err
	}
	tags[0].Name, tags[0].UpdatedAt = name, now
	view := adminTagView(&tags[0])
	return &view, nil
}

// MergeTag 单事务去重迁移关联、下架源标签，并追加受影响帖子的最新快照。
func (s *AdminService) MergeTag(
	ctx context.Context,
	sourceID uint64,
	actorID uint64,
	input MergeAdminTagInput,
) (*AdminTagMergeResult, error) {
	if input.TargetTagID == 0 {
		return nil, apierr.InvalidField(
			"target_tag_id", apierr.FieldInvalidFormat, "target_tag_id 必须是正整数",
		)
	}
	if sourceID == input.TargetTagID {
		return nil, apierr.InvalidField(
			"target_tag_id", apierr.FieldConflict, "源标签与目标标签不能相同",
		)
	}
	tags, err := s.admin.LockTagsByIDs(ctx, []uint64{sourceID, input.TargetTagID})
	if err != nil {
		return nil, apierr.Internal(err)
	}
	if len(tags) != 2 {
		return nil, apierr.NotFound(apierr.BizTagNotFound, "源标签或目标标签")
	}
	byID := map[uint64]*model.Tag{tags[0].ID: &tags[0], tags[1].ID: &tags[1]}
	source, target := byID[sourceID], byID[input.TargetTagID]
	if target.DeletedAt != nil {
		return nil, apierr.Conflict(apierr.BizTagMergeTargetInvalid, "目标标签已下架，请先恢复")
	}
	affected, err := s.prepareTagAffectedPosts(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if err := s.admin.MergeTagRelations(ctx, sourceID, target.ID, now); err != nil {
		return nil, repository.ToAPIError(err, apierr.BizTagNotFound, "标签")
	}
	if err := s.appendTagChangeHistories(
		ctx, affected, actorID, now,
		fmt.Sprintf("话题标签 #%d 合并到 #%d", sourceID, target.ID),
	); err != nil {
		return nil, err
	}
	source.DeletedAt, source.UpdatedAt = &now, now
	return &AdminTagMergeResult{
		Source: adminTagView(source), Target: adminTagView(target),
		AffectedPostCount: len(affected),
	}, nil
}

// DeleteTag 软删除标签，保留全部帖子关联。
func (s *AdminService) DeleteTag(ctx context.Context, tagID uint64) (*AdminTagView, error) {
	return s.setTagDeleted(ctx, tagID, true)
}

// RestoreTag 清空软删除标记，使既有关联自动重新可见。
func (s *AdminService) RestoreTag(ctx context.Context, tagID uint64) (*AdminTagView, error) {
	return s.setTagDeleted(ctx, tagID, false)
}

// HotTags 实时统计未下架标签在未删除已发布帖子中的关联数。
func (s *AdminService) HotTags(ctx context.Context, limit int) (*AdminHotTagList, error) {
	rows, err := s.admin.FindHotTags(ctx, limit)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	items := make([]AdminHotTagView, 0, len(rows))
	for _, row := range rows {
		items = append(items, AdminHotTagView{ID: row.ID, Name: row.Name, PostCount: row.PostCount})
	}
	return &AdminHotTagList{Tags: items}, nil
}

func (s *AdminService) setTagDeleted(
	ctx context.Context,
	tagID uint64,
	deleted bool,
) (*AdminTagView, error) {
	tags, err := s.admin.LockTagsByIDs(ctx, []uint64{tagID})
	if err != nil {
		return nil, apierr.Internal(err)
	}
	if len(tags) != 1 {
		return nil, apierr.NotFound(apierr.BizTagNotFound, "标签")
	}
	now := time.Now().UTC()
	var deletedAt *time.Time
	if deleted {
		deletedAt = &now
	}
	if err := s.admin.SetTagDeletedAt(ctx, tagID, deletedAt, now); err != nil {
		return nil, repository.ToAPIError(err, apierr.BizTagNotFound, "标签")
	}
	tags[0].DeletedAt, tags[0].UpdatedAt = deletedAt, now
	view := adminTagView(&tags[0])
	return &view, nil
}

func (s *AdminService) prepareTagAffectedPosts(
	ctx context.Context,
	tagID uint64,
) ([]tagAffectedPost, error) {
	postIDs, err := s.admin.FindTagPostIDs(ctx, tagID)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	posts, err := s.admin.LockPostsByIDs(ctx, postIDs)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	affected := make([]tagAffectedPost, 0, len(posts))
	for index := range posts {
		latest, latestErr := s.posts.LatestHistory(ctx, posts[index].ID)
		if latestErr != nil {
			return nil, postRepositoryError(latestErr)
		}
		if err := assertPostCurrentMatchesLatest(ctx, s.posts, &posts[index], latest); err != nil {
			return nil, err
		}
		affected = append(affected, tagAffectedPost{post: posts[index], latest: *latest})
	}
	return affected, nil
}

func (s *AdminService) appendTagChangeHistories(
	ctx context.Context,
	affected []tagAffectedPost,
	actorID uint64,
	now time.Time,
	reason string,
) error {
	for index := range affected {
		post := &affected[index].post
		relations, err := s.posts.LoadSnapshotRelations(ctx, post.ID)
		if err != nil {
			return apierr.Internal(err)
		}
		snapshot, err := json.Marshal(snapshotFromCurrent(post, relations))
		if err != nil {
			return apierr.Internal(err)
		}
		if err := s.posts.UpdateContent(ctx, post.ID, map[string]any{"updated_at": now}); err != nil {
			return postRepositoryError(err)
		}
		history := &model.PostHistory{
			PostID: post.ID, Revision: affected[index].latest.Revision + 1,
			EditedBy: actorID, EditedAt: now, Snapshot: snapshot, EditReason: &reason,
		}
		if err := s.posts.CreateHistory(ctx, history); err != nil {
			return historyWriteError(err)
		}
	}
	return nil
}

func adminTagFilter(input AdminTagListInput) (repository.AdminTagFilter, error) {
	filter := repository.AdminTagFilter{IsDeleted: input.IsDeleted}
	name := strings.TrimSpace(norm.NFKC.String(input.Name))
	if name != "" {
		filter.Name = &name
	}
	moderation := strings.TrimSpace(input.Moderation)
	if moderation == "" {
		return filter, nil
	}
	status := model.ModerationStatus(moderation)
	if status != model.ModerationStatusPending && status != model.ModerationStatusPass &&
		status != model.ModerationStatusReview && status != model.ModerationStatusBlock {
		return filter, apierr.InvalidField(
			"moderation", apierr.FieldInvalidEnum,
			"moderation 必须是 pending、pass、review 或 block",
		)
	}
	filter.Moderation = &status
	return filter, nil
}

func normalizeAdminTagName(raw string) (string, error) {
	values := snapshotTags(normalizeTags([]string{raw}))
	if len(values) != 1 || values[0] == "" {
		return "", apierr.InvalidField("name", apierr.FieldRequired, "name 不能为空")
	}
	if utf8.RuneCountInString(values[0]) > maxTagRunes {
		return "", apierr.InvalidField("name", apierr.FieldTooLong, "name 不能超过 10 个字符")
	}
	return values[0], nil
}

func adminTagView(tag *model.Tag) AdminTagView {
	return AdminTagView{
		ID: tag.ID, Name: tag.Name, Moderation: tag.Moderation,
		IsDeleted: tag.DeletedAt != nil, DeletedAt: ptime.Ptr(tag.DeletedAt),
		CreatedAt: ptime.Time(tag.CreatedAt), UpdatedAt: ptime.Time(tag.UpdatedAt),
	}
}
