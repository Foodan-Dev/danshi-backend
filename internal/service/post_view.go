package service

import (
	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/pkg/money"
	"github.com/Foodan-Dev/danshi-backend/internal/pkg/ptime"
	"github.com/Foodan-Dev/danshi-backend/internal/repository"
)

func buildPostListItem(
	record *repository.PostRecord,
	relations repository.PostRelations,
	currentUserID uint64,
) PostListItem {
	flavors := make([]string, 0)
	for _, row := range relations.Flavors[record.ID] {
		if row.Stance == model.FlavorStanceHas {
			flavors = append(flavors, row.Name)
		}
	}
	var price *money.Amount
	if record.Price != nil {
		amount := money.FromDecimal(*record.Price)
		price = &amount
	}
	images := nonNilStrings(relations.Images[record.ID])
	imageDisplays, imageThumbs := deriveImageTiers(images)
	return PostListItem{
		ID: record.ID, PostType: record.PostType, ShareType: record.ShareType,
		Title: record.Title, Content: record.Content, Category: record.Category,
		Canteen: buildCanteen(record), CanteenWindow: buildWindow(record), Cuisine: record.CuisineName,
		Flavors: flavors, Tags: nonNilStrings(relations.Tags[record.ID]), Price: price,
		Images: images, ImageDisplays: imageDisplays, ImageThumbs: imageThumbs,
		Author: buildPostAuthor(record, relations, currentUserID),
		Stats: PostStatsView{
			LikeCount: record.LikeCount, FavoriteCount: record.FavoriteCount,
			CommentCount: record.CommentCount, ViewCount: record.ViewCount,
		},
		IsLiked: relations.Liked[record.ID], IsFavorited: relations.Favorited[record.ID],
		IsEdited: record.Revision > 0, Status: record.Status,
		CreatedAt: ptime.Time(record.CreatedAt), UpdatedAt: ptime.Time(record.UpdatedAt),
	}
}

func buildCanteen(record *repository.PostRecord) *CanteenView {
	if record.CanteenCode == nil || record.CanteenName == nil || record.CanteenCampus == nil {
		return nil
	}
	return &CanteenView{Code: *record.CanteenCode, Name: *record.CanteenName, Campus: *record.CanteenCampus}
}

func buildWindow(record *repository.PostRecord) *CanteenWindowView {
	if record.CanteenWindowID == nil || record.WindowName == nil {
		return nil
	}
	return &CanteenWindowView{ID: *record.CanteenWindowID, Name: *record.WindowName, Floor: record.WindowFloor}
}

func buildPostAuthor(
	record *repository.PostRecord,
	relations repository.PostRelations,
	currentUserID uint64,
) PostAuthorView {
	name := record.AuthorName
	avatarURL := record.AvatarURL
	if record.AuthorDeletedAt != nil {
		name = "已注销用户"
		avatarURL = nil
	}
	var following *bool
	if record.AuthorID != currentUserID {
		value := relations.Following[record.AuthorID]
		following = &value
	}
	return PostAuthorView{
		ID: record.AuthorID, Name: name, AvatarURL: avatarURL,
		AvatarThumbURL: avatarThumbURL(avatarURL), IsFollowing: following,
	}
}

func buildPreferences(rows []repository.PostFlavorRow) *PreferencesView {
	preferences := &PreferencesView{AvoidFlavors: []string{}, PreferFlavors: []string{}}
	for _, row := range rows {
		switch row.Stance {
		case model.FlavorStanceAvoid:
			preferences.AvoidFlavors = append(preferences.AvoidFlavors, row.Name)
		case model.FlavorStancePrefer:
			preferences.PreferFlavors = append(preferences.PreferFlavors, row.Name)
		}
	}
	if len(preferences.AvoidFlavors) == 0 && len(preferences.PreferFlavors) == 0 {
		return nil
	}
	return preferences
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
