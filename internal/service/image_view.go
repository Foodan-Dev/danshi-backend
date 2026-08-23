package service

import (
	"net/url"
	"strings"

	"github.com/Foodan-Dev/danshi-backend/internal/model"
)

const (
	imageThumbProcessingQuery   = "imageMogr2/thumbnail/720x720>/format/webp/quality/80"
	imageDisplayProcessingQuery = "imageMogr2/thumbnail/1440x1440>/format/webp/quality/85"
)

// deriveImageTiers 只为公开图片地址构造实时处理档位，并保持输入顺序与长度。
func deriveImageTiers(images []string) (displays []string, thumbs []string) {
	displays = make([]string, len(images))
	thumbs = make([]string, len(images))
	for index, image := range images {
		parsed, err := url.Parse(image)
		if image == "" || model.IsPurgedImageURL(image) || err != nil ||
			!derivableImageURL(parsed) {
			displays[index], thumbs[index] = image, image
			continue
		}

		derive := func(processingQuery string) string {
			derived := *parsed
			if derived.RawQuery == "" {
				derived.RawQuery = processingQuery
			} else {
				derived.RawQuery += "&" + processingQuery
			}
			return derived.String()
		}
		displays[index] = derive(imageDisplayProcessingQuery)
		thumbs[index] = derive(imageThumbProcessingQuery)
	}
	return displays, thumbs
}

// derivableImageURL 判定一个地址能否安全地追加实时处理参数。
//
// 判据刻意与供应商无关。原先只按 COS 的 q-signature 参数名识别签名 URL，
// 但本地存储签发的是 memory://...?signature=，同一段代码对两家供应商给出
// 不同结论——一个只防住一家的守卫，比没有守卫更容易让人以为已经防住了。
// 改为匹配任何名字里带 signature 的查询参数（q-signature、signature、
// X-Amz-Signature 均覆盖），并排除非 http(s) 地址。
// 带普通查询参数的公开地址（如 ?version=7）仍然可以派生。
func derivableImageURL(parsed *url.URL) bool {
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return false
	}
	for key := range parsed.Query() {
		if strings.Contains(strings.ToLower(key), "signature") {
			return false
		}
	}
	return true
}

func avatarThumbURL(avatarURL *string) *string {
	if avatarURL == nil {
		return nil
	}
	_, thumbs := deriveImageTiers([]string{*avatarURL})
	return &thumbs[0]
}
