package bottools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const usageDir = ".run/usage"

type usageRecord struct {
	Time         string  `json:"time"`
	CostYuan     float64 `json:"cost_yuan"`
	TotalTokens  int64   `json:"total_tokens"`
	PromptTokens int64   `json:"prompt_tokens"`
}

type dayBilling struct {
	Date        string
	TotalYuan   float64
	RecordCount int
	TotalTokens int64
}

type groupBilling struct {
	GroupID     int64
	TotalYuan   float64
	RecordCount int
	TotalTokens int64
	ByDay       []dayBilling
}

type groupIDKey struct{}

func WithGroupID(ctx context.Context, groupID int64) context.Context {
	return context.WithValue(ctx, groupIDKey{}, groupID)
}

func groupIDFromContext(ctx context.Context) (int64, bool) {
	id, ok := ctx.Value(groupIDKey{}).(int64)
	return id, ok && id > 0
}

func recordDate(timeStr string) string {
	if timeStr == "" {
		return "（无日期）"
	}
	t, err := time.Parse(time.RFC3339, timeStr)
	if err != nil {
		return "（无日期）"
	}
	return t.Format("2006-01-02")
}

func sumGroupBillingFile(path string) (groupBilling, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return groupBilling{}, nil
		}
		return groupBilling{}, err
	}
	defer f.Close()

	var sum groupBilling
	dayMap := make(map[string]*dayBilling)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var rec usageRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		sum.TotalYuan += rec.CostYuan
		sum.TotalTokens += rec.TotalTokens
		sum.RecordCount++

		date := recordDate(rec.Time)
		d := dayMap[date]
		if d == nil {
			d = &dayBilling{Date: date}
			dayMap[date] = d
		}
		d.TotalYuan += rec.CostYuan
		d.TotalTokens += rec.TotalTokens
		d.RecordCount++
	}
	if err := sc.Err(); err != nil {
		return groupBilling{}, err
	}
	for _, d := range dayMap {
		sum.ByDay = append(sum.ByDay, *d)
	}
	sort.Slice(sum.ByDay, func(i, j int) bool {
		return sum.ByDay[i].Date < sum.ByDay[j].Date
	})
	return sum, nil
}

func loadGroupBilling(groupID int64) (groupBilling, error) {
	path := filepath.Join(usageDir, fmt.Sprintf("%d.jsonl", groupID))
	sum, err := sumGroupBillingFile(path)
	if err != nil {
		return groupBilling{}, err
	}
	sum.GroupID = groupID
	return sum, nil
}

func loadAllGroupBillings() ([]groupBilling, float64, error) {
	entries, err := os.ReadDir(usageDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, nil
		}
		return nil, 0, err
	}
	var out []groupBilling
	var grandTotal float64
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".jsonl") {
			continue
		}
		idStr := strings.TrimSuffix(ent.Name(), ".jsonl")
		groupID, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil || groupID <= 0 {
			continue
		}
		sum, err := loadGroupBilling(groupID)
		if err != nil {
			return nil, 0, err
		}
		if sum.RecordCount == 0 {
			continue
		}
		out = append(out, sum)
		grandTotal += sum.TotalYuan
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].GroupID < out[j].GroupID
	})
	return out, grandTotal, nil
}

func formatGroupBilling(b groupBilling) string {
	var s strings.Builder
	fmt.Fprintf(&s, "群 %d\n", b.GroupID)
	fmt.Fprintf(&s, "累计：%.4f 元，%d 次调用，共 %d tokens\n", b.TotalYuan, b.RecordCount, b.TotalTokens)
	if len(b.ByDay) > 0 {
		s.WriteString("\n按日：\n")
		for _, d := range b.ByDay {
			fmt.Fprintf(&s, "  %s  %.4f 元  %d 次  %d tokens\n", d.Date, d.TotalYuan, d.RecordCount, d.TotalTokens)
		}
	}
	return strings.TrimRight(s.String(), "\n")
}

func GroupBillText(groupID int64) (string, error) {
	sum, err := loadGroupBilling(groupID)
	if err != nil {
		return "", err
	}
	if sum.RecordCount == 0 {
		return fmt.Sprintf("群 %d 暂无账单记录。", groupID), nil
	}
	return formatGroupBilling(sum), nil
}

func QueryGroupBilling(ctx context.Context, args *struct {
	AllGroups bool `description:"为 true 时列出所有有账单记录的群；为 false 时只查当前群。"`
}) string {
	if args.AllGroups {
		list, grand, err := loadAllGroupBillings()
		if err != nil {
			return fmt.Sprintf("查询账单失败：%v", err)
		}
		if len(list) == 0 {
			return "暂无账单记录（.run/usage 下没有数据）。"
		}
		var b strings.Builder
		b.WriteString("各群累计账单（自进程有记录以来，按 .run/usage 持久化汇总）：\n\n")
		for i, item := range list {
			if i > 0 {
				b.WriteString("\n\n")
			}
			b.WriteString(formatGroupBilling(item))
		}
		fmt.Fprintf(&b, "\n\n全部合计：%.4f 元", grand)
		return b.String()
	}
	groupID, ok := groupIDFromContext(ctx)
	if !ok {
		return "查询账单失败：当前上下文没有群号。"
	}
	sum, err := loadGroupBilling(groupID)
	if err != nil {
		return fmt.Sprintf("查询账单失败：%v", err)
	}
	if sum.RecordCount == 0 {
		return fmt.Sprintf("群 %d 暂无账单记录。", groupID)
	}
	return formatGroupBilling(sum) + "\n（数据来自 .run/usage 持久化记录）"
}
