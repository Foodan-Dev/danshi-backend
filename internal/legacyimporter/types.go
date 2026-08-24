// Package legacyimporter implements the one-off, replayable import from the retired Python database.
package legacyimporter

import "time"

const (
	specialReplyFallbackID = "4a4e232a-63b5-4d7e-b17e-684cd064377c"
	legacyBanReason        = "旧系统账号已停用"
)

type sourceData struct {
	Users         []sourceUser
	Images        []sourceImage
	Posts         []sourcePost
	Comments      []sourceComment
	Likes         []sourceLike
	Favorites     []sourceFavorite
	Follows       []sourceFollow
	Notifications []sourceNotification
	Flavors       []sourceFlavor
}

type sourceUser struct {
	ID, Email, Password, Name string
	Gender, Hometown          *string
	AvatarURL, Bio            *string
	Role                      string
	IsActive                  bool
	CreatedAt, UpdatedAt      time.Time
}

type sourceImage struct {
	ID, Purpose, ObjectKey, PublicURL, ContentType, Status string
	UploaderID                                             *string
	Size                                                   *int64
	CreatedAt, UpdatedAt                                   time.Time
}

type sourcePost struct {
	ID, PostType, Title, Content, Category, AuthorID, Status string
	Canteen, ShareType, Cuisine, Price                       *string
	Tags, Flavors, Images                                    []string
	Preferences                                              *sourcePreferences
	BudgetMin, BudgetMax                                     *int32
	LikeCount, FavoriteCount, CommentCount, ViewCount        int32
	CreatedAt, UpdatedAt                                     time.Time
}

type sourcePreferences struct {
	PreferFlavors []string `json:"prefer_flavors"`
	AvoidFlavors  []string `json:"avoid_flavors"`
}

type sourceComment struct {
	ID, Content, PostID, AuthorID string
	ParentID, ReplyToUserID       *string
	LikeCount, ReplyCount         int32
	CreatedAt, UpdatedAt          time.Time
}

type sourceLike struct {
	ID, UserID, Type, TargetID string
	CreatedAt                  time.Time
}

type sourceFavorite struct {
	ID, UserID, PostID string
	CreatedAt          time.Time
}

type sourceFollow struct {
	ID, FollowerID, FollowingID string
	CreatedAt                   time.Time
}

type sourceNotification struct {
	ID, RecipientID, SenderID, Type string
	RelatedID, RelatedType, Content *string
	IsRead                          bool
	CreatedAt, UpdatedAt            time.Time
}

type sourceFlavor struct {
	ID, Name  string
	IsActive  bool
	SortOrder int32
	CreatedAt time.Time
}

type dictionaries struct {
	Canteens map[string]int64
	Cuisines map[string]dictionaryItem
	Flavors  map[string]dictionaryItem
}

type dictionaryItem struct {
	ID        int64
	IsActive  bool
	SortOrder int32
}

type dataset struct {
	Users         map[int64]userRow
	Roles         map[string]roleRow
	RoleRecords   map[int64]roleRecordRow
	BanRecords    map[int64]banRecordRow
	Images        map[int64]imageRow
	Cuisines      map[int64]dictionaryRow
	Flavors       map[int64]dictionaryRow
	Posts         map[int64]postRow
	Tags          map[int64]tagRow
	PostTags      map[string]postTagRow
	PostFlavors   map[string]postFlavorRow
	PostImages    map[string]postImageRow
	Comments      map[int64]commentRow
	Follows       map[string]followRow
	Favorites     map[string]favoriteRow
	PostLikes     map[string]postLikeRow
	CommentLikes  map[string]commentLikeRow
	Notifications map[int64]notificationRow
	SourceFlavors []sourceFlavor
	Events        []decisionEvent
	Stats         map[string]tableStat
}

type userRow struct {
	SourceID           string     `verify:"-"`
	ID                 int64      `verify:"id"`
	Email              string     `verify:"email"`
	PasswordHash       string     `verify:"password_hash"`
	Name               string     `verify:"name"`
	Gender             *string    `verify:"gender"`
	Bio                *string    `verify:"bio"`
	AvatarImageAssetID *int64     `verify:"avatar_image_asset_id"`
	BanIsPermanent     bool       `verify:"ban_is_permanent"`
	BannedUntil        *time.Time `verify:"banned_until"`
	BanReason          *string    `verify:"ban_reason"`
	BannedBy           *int64     `verify:"banned_by"`
	DeletedAt          *time.Time `verify:"deleted_at"`
	CreatedAt          time.Time  `verify:"created_at"`
	UpdatedAt          time.Time  `verify:"updated_at"`
}

type roleRow struct {
	SourceID  string    `verify:"-"`
	UserID    int64     `verify:"user_id"`
	Role      string    `verify:"role"`
	GrantedBy *int64    `verify:"granted_by"`
	GrantedAt time.Time `verify:"granted_at"`
}

type roleRecordRow struct {
	SourceID  string    `verify:"-"`
	ID        int64     `verify:"id"`
	UserID    int64     `verify:"user_id"`
	Role      string    `verify:"role"`
	Action    string    `verify:"action"`
	ActorID   *int64    `verify:"actor_id"`
	CreatedAt time.Time `verify:"created_at"`
}

type banRecordRow struct {
	SourceID     string     `verify:"-"`
	ID           int64      `verify:"id"`
	UserID       int64      `verify:"user_id"`
	Action       string     `verify:"action"`
	BanPermanent bool       `verify:"ban_is_permanent"`
	BannedUntil  *time.Time `verify:"banned_until"`
	Reason       *string    `verify:"reason"`
	ActorID      *int64     `verify:"actor_id"`
	CreatedAt    time.Time  `verify:"created_at"`
}

type imageRow struct {
	SourceID    string    `verify:"-"`
	ID          int64     `verify:"id"`
	UploaderID  *int64    `verify:"uploader_id"`
	Purpose     string    `verify:"purpose"`
	ObjectKey   string    `verify:"object_key"`
	PublicURL   string    `verify:"public_url"`
	ContentType string    `verify:"content_type"`
	Size        *int64    `verify:"size"`
	Status      string    `verify:"status"`
	Moderation  string    `verify:"moderation"`
	CreatedAt   time.Time `verify:"created_at"`
	UpdatedAt   time.Time `verify:"updated_at"`
}

type dictionaryRow struct {
	SourceID  string    `verify:"-"`
	ID        int64     `verify:"id"`
	Name      string    `verify:"name"`
	SortOrder int32     `verify:"sort_order"`
	IsActive  bool      `verify:"is_active"`
	CreatedAt time.Time `verify:"created_at"`
	UpdatedAt time.Time `verify:"updated_at"`
}

type postRow struct {
	SourceID      string     `verify:"-"`
	ID            int64      `verify:"id"`
	AuthorID      int64      `verify:"author_id"`
	PostType      string     `verify:"post_type"`
	ShareType     *string    `verify:"share_type"`
	Status        string     `verify:"status"`
	Category      string     `verify:"category"`
	Title         string     `verify:"title"`
	Content       string     `verify:"content"`
	CanteenID     *int64     `verify:"canteen_id"`
	CuisineID     *int64     `verify:"cuisine_id"`
	Price         *string    `verify:"price"`
	BudgetMin     *int32     `verify:"budget_min"`
	BudgetMax     *int32     `verify:"budget_max"`
	LikeCount     int32      `verify:"like_count"`
	FavoriteCount int32      `verify:"favorite_count"`
	CommentCount  int32      `verify:"comment_count"`
	ViewCount     int32      `verify:"view_count"`
	DeletedAt     *time.Time `verify:"deleted_at"`
	DeletedReason *string    `verify:"deleted_reason"`
	DeletedBy     *int64     `verify:"deleted_by"`
	CreatedAt     time.Time  `verify:"created_at"`
	UpdatedAt     time.Time  `verify:"updated_at"`
}

type tagRow struct {
	SourceID   string     `verify:"-"`
	ID         int64      `verify:"id"`
	Name       string     `verify:"name"`
	Moderation string     `verify:"moderation"`
	DeletedAt  *time.Time `verify:"deleted_at"`
	CreatedAt  time.Time  `verify:"created_at"`
	UpdatedAt  time.Time  `verify:"updated_at"`
}

type postTagRow struct {
	SourceID string `verify:"-"`
	PostID   int64  `verify:"post_id"`
	TagID    int64  `verify:"tag_id"`
}

type postFlavorRow struct {
	SourceID string `verify:"-"`
	PostID   int64  `verify:"post_id"`
	FlavorID int64  `verify:"flavor_id"`
	Stance   string `verify:"stance"`
	PostType string `verify:"post_type"`
}

type postImageRow struct {
	SourceID     string    `verify:"-"`
	PostID       int64     `verify:"post_id"`
	Position     int16     `verify:"position"`
	ImageAssetID int64     `verify:"image_asset_id"`
	CreatedAt    time.Time `verify:"created_at"`
}

type commentRow struct {
	SourceID      string     `verify:"-"`
	ID            int64      `verify:"id"`
	PostID        int64      `verify:"post_id"`
	AuthorID      int64      `verify:"author_id"`
	ParentID      *int64     `verify:"parent_id"`
	RootID        *int64     `verify:"root_id"`
	ReplyToUserID int64      `verify:"reply_to_user_id"`
	Content       string     `verify:"content"`
	Moderation    string     `verify:"moderation"`
	LikeCount     int32      `verify:"like_count"`
	ReplyCount    int32      `verify:"reply_count"`
	DeletedAt     *time.Time `verify:"deleted_at"`
	DeletedReason *string    `verify:"deleted_reason"`
	DeletedBy     *int64     `verify:"deleted_by"`
	CreatedAt     time.Time  `verify:"created_at"`
	UpdatedAt     time.Time  `verify:"updated_at"`
}

type followRow struct {
	SourceID    string    `verify:"-"`
	FollowerID  int64     `verify:"follower_id"`
	FollowingID int64     `verify:"following_id"`
	CreatedAt   time.Time `verify:"created_at"`
}

type favoriteRow struct {
	SourceID  string    `verify:"-"`
	UserID    int64     `verify:"user_id"`
	PostID    int64     `verify:"post_id"`
	CreatedAt time.Time `verify:"created_at"`
}

type postLikeRow struct {
	SourceID  string    `verify:"-"`
	UserID    int64     `verify:"user_id"`
	PostID    int64     `verify:"post_id"`
	CreatedAt time.Time `verify:"created_at"`
}

type commentLikeRow struct {
	SourceID  string    `verify:"-"`
	UserID    int64     `verify:"user_id"`
	CommentID int64     `verify:"comment_id"`
	CreatedAt time.Time `verify:"created_at"`
}

type notificationRow struct {
	SourceID         string    `verify:"-"`
	ID               int64     `verify:"id"`
	RecipientID      int64     `verify:"recipient_id"`
	SenderID         int64     `verify:"sender_id"`
	Type             string    `verify:"type"`
	RelatedPostID    *int64    `verify:"related_post_id"`
	RelatedCommentID *int64    `verify:"related_comment_id"`
	Content          *string   `verify:"content"`
	IsRead           bool      `verify:"is_read"`
	CreatedAt        time.Time `verify:"created_at"`
	UpdatedAt        time.Time `verify:"updated_at"`
}

type decisionEvent struct {
	Kind, Table, SourceID, Field, Code string
}

type tableStat struct {
	SourceRows, TargetRows, OmittedRows int
}

type mismatch struct {
	Table, SourceID, Field, Code string
}

// Failure is deliberately value-free: database errors must never echo email, password, or content.
type Failure struct {
	Code, Table, SourceID, Field string
	cause                        error
}

func (f *Failure) Error() string {
	message := "legacy_import_error code=" + f.Code
	if f.Table != "" {
		message += " table=" + f.Table
	}
	if f.SourceID != "" {
		message += " source_id=" + f.SourceID
	}
	if f.Field != "" {
		message += " field=" + f.Field
	}
	return message
}

func failure(code, table string, cause error) error {
	return &Failure{Code: code, Table: table, cause: cause}
}

func rowFailure(code, table, sourceID, field string) error {
	return &Failure{Code: code, Table: table, SourceID: sourceID, Field: field}
}
