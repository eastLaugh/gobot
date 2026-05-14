package bottools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

const steamAPIBase = "https://api.steampowered.com"

var steamHTTP = &http.Client{Timeout: 10 * time.Second}

var (
	reSteamID64  = regexp.MustCompile(`^7656119\d{10}$`)
	reProfileURL = regexp.MustCompile(`steamcommunity\.com/(?:id|profiles)/([^/?#\s]+)`)
)

type steamPlayer struct {
	SteamID       string `json:"steamid"`
	PersonaName   string `json:"personaname"`
	ProfileURL    string `json:"profileurl"`
	PersonaState  int    `json:"personastate"`
	CommunityVis  int    `json:"communityvisibilitystate"`
	LastLogoff    int64  `json:"lastlogoff"`
	GameExtraInfo string `json:"gameextrainfo"`
	GameID        string `json:"gameid"`
	GameServerIP  string `json:"gameserverip"`
}

type steamSummariesResp struct {
	Response struct {
		Players []steamPlayer `json:"players"`
	} `json:"response"`
}

type steamResolveResp struct {
	Response struct {
		SteamID string `json:"steamid"`
		Success int    `json:"success"`
		Message string `json:"message"`
	} `json:"response"`
}

func personaStateText(s int) string {
	switch s {
	case 0:
		return "离线"
	case 1:
		return "在线"
	case 2:
		return "忙碌"
	case 3:
		return "离开"
	case 4:
		return "打盹"
	case 5:
		return "想交易"
	case 6:
		return "想开黑"
	default:
		return fmt.Sprintf("未知(%d)", s)
	}
}

func normalizeSteamInput(in string) (kind, val string) {
	in = strings.TrimSpace(in)
	if in == "" {
		return "", ""
	}
	if m := reProfileURL.FindStringSubmatch(in); m != nil {
		in = m[1]
	}
	if reSteamID64.MatchString(in) {
		return "id", in
	}
	return "vanity", in
}

func resolveVanity(ctx context.Context, apiKey, vanity string) (string, error) {
	u, _ := url.Parse(steamAPIBase + "/ISteamUser/ResolveVanityURL/v1/")
	q := u.Query()
	q.Set("key", apiKey)
	q.Set("vanityurl", vanity)
	u.RawQuery = q.Encode()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	resp, err := steamHTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<10))
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var r steamResolveResp
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", err
	}
	if r.Response.Success != 1 || r.Response.SteamID == "" {
		return "", fmt.Errorf("vanity %q 未找到 (success=%d %s)", vanity, r.Response.Success, r.Response.Message)
	}
	return r.Response.SteamID, nil
}

func getPlayerSummaries(ctx context.Context, apiKey, steamID string) (*steamPlayer, error) {
	u, _ := url.Parse(steamAPIBase + "/ISteamUser/GetPlayerSummaries/v2/")
	q := u.Query()
	q.Set("key", apiKey)
	q.Set("steamids", steamID)
	u.RawQuery = q.Encode()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	resp, err := steamHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<10))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var r steamSummariesResp
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, err
	}
	if len(r.Response.Players) == 0 {
		return nil, fmt.Errorf("SteamID %s 不存在或被隐藏", steamID)
	}
	return &r.Response.Players[0], nil
}

func QuerySteamStatus(ctx context.Context, args *struct {
	ID string `description:"Steam 玩家标识，可以是：17 位 SteamID64 数字、vanity 自定义 URL 段（如 gabelogannewell）、或完整的 steamcommunity.com 主页链接。工具会自动识别。"`
}) string {
	apiKey := strings.TrimSpace(os.Getenv("STEAM_API_KEY"))
	if apiKey == "" {
		return "未配置 STEAM_API_KEY，无法查询 Steam 状态。"
	}

	kind, val := normalizeSteamInput(args.ID)
	if val == "" {
		return "Steam 查询失败：输入为空。"
	}

	steamID := val
	if kind == "vanity" {
		resolved, err := resolveVanity(ctx, apiKey, val)
		if err != nil {
			return fmt.Sprintf("Steam 查询失败（vanity 解析）：%v", err)
		}
		steamID = resolved
	}

	p, err := getPlayerSummaries(ctx, apiKey, steamID)
	if err != nil {
		return fmt.Sprintf("Steam 查询失败：%v", err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Steam 玩家 %s\n", p.PersonaName)
	fmt.Fprintf(&b, "- SteamID: %s\n", p.SteamID)
	fmt.Fprintf(&b, "- 主页: %s\n", strings.TrimRight(p.ProfileURL, "/"))
	fmt.Fprintf(&b, "- 状态: %s", personaStateText(p.PersonaState))
	if p.GameExtraInfo != "" {
		fmt.Fprintf(&b, "（正在玩 %s", p.GameExtraInfo)
		if p.GameID != "" {
			fmt.Fprintf(&b, "，appid %s", p.GameID)
		}
		b.WriteString("）")
	}
	b.WriteString("\n")
	if p.CommunityVis != 3 {
		b.WriteString("- 个人资料非公开，可能信息不全。\n")
	}
	if p.PersonaState == 0 && p.LastLogoff > 0 {
		t := time.Unix(p.LastLogoff, 0).Format("2006-01-02 15:04:05")
		fmt.Fprintf(&b, "- 上次在线: %s\n", t)
	}
	return strings.TrimRight(b.String(), "\n")
}
