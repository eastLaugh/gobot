package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const leaderboardAPIBase = "https://api.the-finals-leaderboard.com"

// 默认查询当前赛季 S10 跨平台榜。后续 The Finals 出新赛季时改这里。
const currentSeasonPath = "/v1/leaderboard/s10/crossplay"

type leaderboardEntry struct {
	Rank           int    `json:"rank"`
	Change         int    `json:"change"`
	Name           string `json:"name"`
	SteamName      string `json:"steamName"`
	PsnName        string `json:"psnName"`
	XboxName       string `json:"xboxName"`
	ClubTag        string `json:"clubTag"`
	LeagueNumber   int    `json:"leagueNumber"`
	League         string `json:"league"`
	RankScore      int    `json:"rankScore"`
	TournamentWins int    `json:"tournamentWins"`
}

type leaderboardResp struct {
	Meta  map[string]any     `json:"meta"`
	Count int                `json:"count"`
	Data  []leaderboardEntry `json:"data"`
}

var leaderboardHTTP = &http.Client{Timeout: 15 * time.Second}

func QueryFinalsPlayer(ctx context.Context, args *struct {
	Name string `description:"玩家昵称，子串模糊匹配，不区分大小写。通常形如「Eastlaugh#1234」。注意：API 只收录每赛季前 10000 名（约 Platinum 2 以上），低于此门槛会返回未找到，这是正常的，不代表玩家不存在。"`
}) string {
	name := strings.TrimSpace(args.Name)
	if name == "" {
		return "查询失败：玩家名为空"
	}

	u, _ := url.Parse(leaderboardAPIBase + currentSeasonPath)
	q := u.Query()
	q.Set("name", name)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return fmt.Sprintf("构造请求失败：%v", err)
	}
	resp, err := leaderboardHTTP.Do(req)
	if err != nil {
		return fmt.Sprintf("查询失败（网络错误）：%v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<10))
		return fmt.Sprintf("查询失败（HTTP %d）：%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var lr leaderboardResp
	if err := json.NewDecoder(resp.Body).Decode(&lr); err != nil {
		return fmt.Sprintf("查询失败（解析错误）：%v", err)
	}

	if lr.Count == 0 {
		return fmt.Sprintf("玩家 %q 不在本赛季 (S10 跨平台) 排行榜的前 10000 名。可能是名字拼写不一致，或本赛季排位分未上榜（门槛约 Platinum 2 以上）。", name)
	}

	exact := -1
	for i, e := range lr.Data {
		if strings.EqualFold(e.Name, name) {
			exact = i
			break
		}
	}

	var picked []leaderboardEntry
	if exact >= 0 {
		picked = []leaderboardEntry{lr.Data[exact]}
	} else if len(lr.Data) <= 3 {
		picked = lr.Data
	} else {
		picked = lr.Data[:3]
	}

	var b strings.Builder
	if exact >= 0 {
		fmt.Fprintf(&b, "S10 排行榜查到玩家 %s：\n", picked[0].Name)
	} else {
		fmt.Fprintf(&b, "S10 排行榜共 %d 个匹配，显示前 %d 个：\n", lr.Count, len(picked))
	}
	for _, e := range picked {
		fmt.Fprintf(&b, "- 排名 #%d", e.Rank)
		if e.Change != 0 {
			fmt.Fprintf(&b, "（变化 %+d）", e.Change)
		}
		fmt.Fprintf(&b, " | %s", e.Name)
		if e.ClubTag != "" {
			fmt.Fprintf(&b, " [%s]", e.ClubTag)
		}
		fmt.Fprintf(&b, " | 段位 %s | 分数 %d", e.League, e.RankScore)
		var aliases []string
		if e.SteamName != "" {
			aliases = append(aliases, "Steam:"+e.SteamName)
		}
		if e.PsnName != "" {
			aliases = append(aliases, "PSN:"+e.PsnName)
		}
		if e.XboxName != "" {
			aliases = append(aliases, "Xbox:"+e.XboxName)
		}
		if len(aliases) > 0 {
			fmt.Fprintf(&b, " | %s", strings.Join(aliases, " "))
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
