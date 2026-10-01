package httpx

import (
	"sort"
	"strconv"
	"strings"
)

// 支持的界面语言（D-026）。前端壳的语言清单与这里一致。
const (
	LangZH   = "zh-CN" // 简体中文（默认语言）
	LangEN   = "en-US" // English
	LangZHTW = "zh-TW" // 繁體中文
	LangJA   = "ja-JP" // 日本語
	LangKO   = "ko-KR" // 한국어
	LangMS   = "ms-MY" // Bahasa Melayu
	LangTA   = "ta-IN" // தமிழ்
	LangBN   = "bn-BD" // বাংলা
	LangRU   = "ru-RU" // Русский
	LangFR   = "fr-FR" // Français
	LangDE   = "de-DE" // Deutsch
)

// DefaultLang 是没有 Accept-Language 时用的语言，也是字典等数据的默认语言。
const DefaultLang = LangZH

// Langs 返回全部支持的语言代码。
func Langs() []string {
	return []string{LangEN, LangZH, LangZHTW, LangJA, LangKO, LangMS, LangTA, LangBN, LangRU, LangFR, LangDE}
}

// FallbackLangs 返回某种语言缺文案时依次尝试的语言：繁体先简体再英文，其他语言英文，最后默认语言。
func FallbackLangs(lang string) []string {
	switch lang {
	case LangZHTW:
		return []string{LangZH, LangEN}
	case LangEN:
		return []string{LangZH}
	case LangZH:
		return []string{LangEN}
	default:
		return []string{LangEN, LangZH}
	}
}

// MatchLang 把一个语言标签（zh-Hant-HK、ta-SG、de 这类）匹配到支持的语言；匹配不上返回空串。
func MatchLang(tag string) string {
	t := strings.ToLower(strings.TrimSpace(strings.ReplaceAll(tag, "_", "-")))
	if t == "" {
		return ""
	}
	for _, l := range Langs() {
		if strings.ToLower(l) == t {
			return l
		}
	}
	parts := strings.Split(t, "-")
	if parts[0] == "zh" {
		for _, p := range parts[1:] {
			if p == "hant" || p == "tw" || p == "hk" || p == "mo" {
				return LangZHTW
			}
		}
		return LangZH
	}
	for _, l := range Langs() {
		if strings.HasPrefix(strings.ToLower(l), parts[0]+"-") {
			return l
		}
	}
	return ""
}

// ParseAcceptLanguage 按 q 值从高到低逐个匹配支持的语言：都不匹配时用英文；头为空时用默认语言。
func ParseAcceptLanguage(header string) string {
	header = strings.TrimSpace(header)
	if header == "" {
		return DefaultLang
	}
	type cand struct {
		tag string
		q   float64
		i   int
	}
	var cs []cand
	for i, part := range strings.Split(header, ",") {
		fields := strings.Split(part, ";")
		c := cand{tag: strings.TrimSpace(fields[0]), q: 1, i: i}
		for _, f := range fields[1:] {
			f = strings.TrimSpace(f)
			if v, ok := strings.CutPrefix(f, "q="); ok {
				if q, err := strconv.ParseFloat(v, 64); err == nil {
					c.q = q
				}
			}
		}
		if c.q > 0 {
			cs = append(cs, c)
		}
	}
	sort.SliceStable(cs, func(a, b int) bool { return cs[a].q > cs[b].q })
	for _, c := range cs {
		if l := MatchLang(c.tag); l != "" {
			return l
		}
	}
	return LangEN
}
