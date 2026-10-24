package main

import (
	"testing"
	"time"

	"github.com/asanilo/workbuddy-tasks/internal/upstream"
)

// pkg 构造一个测试用资源包；end 为零值时表示上游没给到期时间。
func pkg(name string, remain int64, end time.Time) upstream.CreditPackage {
	p := upstream.CreditPackage{Name: name, Remain: remain, Size: remain}
	if !end.IsZero() {
		p.ExpiresAt = end.UnixMilli()
		p.EndTime = end.In(creditZone).Format(time.RFC3339)
	}
	return p
}

func TestSummarizeCredits(t *testing.T) {
	now := time.Now()
	packs := []upstream.CreditPackage{
		pkg("soon", 100, now.Add(72*time.Hour)),
		pkg("later", 200, now.Add(240*time.Hour)),
		pkg("expired", 50, now.Add(-24*time.Hour)),
		pkg("zero-remain", 0, now.Add(48*time.Hour)),
		pkg("no-end", 30, time.Time{}),
	}
	got := summarizeCredits(packs, 330, 400, 7, false)
	if got.Remain != 330 || got.Total != 400 {
		t.Fatalf("remain/total = %d/%d, want 330/400", got.Remain, got.Total)
	}
	if got.SoonRemain != 100 {
		t.Errorf("soon_remain = %d, want 100（只算 7 天内且剩余 > 0）", got.SoonRemain)
	}
	if len(got.Buckets) != 2 {
		t.Errorf("buckets = %d, want 2（过期包与零余额包不计）: %+v", len(got.Buckets), got.Buckets)
	}
	wantEarliest := now.Add(72 * time.Hour).In(creditZone).Format("2006-01-02")
	if got.Earliest != wantEarliest {
		t.Errorf("earliest = %q, want %q", got.Earliest, wantEarliest)
	}
	if got.EarliestDays < 2 || got.EarliestDays > 3 {
		t.Errorf("earliest_days = %d, want 2~3", got.EarliestDays)
	}
	if got.Packages != nil {
		t.Errorf("未请求逐包明细时不应输出 packages: %+v", got.Packages)
	}
}

func TestSummarizeCreditsWithPackages(t *testing.T) {
	now := time.Now()
	packs := []upstream.CreditPackage{
		pkg("b", 10, now.Add(48*time.Hour)),
		pkg("a", 20, now.Add(24*time.Hour)),
		pkg("zero", 0, now.Add(24*time.Hour)),
	}
	got := summarizeCredits(packs, 30, 30, 7, true)
	if len(got.Packages) != 2 {
		t.Fatalf("packages = %d, want 2（零余额包不输出）", len(got.Packages))
	}
	if got.Packages[0].Name != "a" || got.Packages[1].Name != "b" {
		t.Errorf("逐包顺序 = %s,%s，want a,b（按到期升序）", got.Packages[0].Name, got.Packages[1].Name)
	}
}

func TestDaysUntil(t *testing.T) {
	base := time.Date(2026, 10, 24, 23, 30, 0, 0, creditZone)
	cases := []struct {
		to   time.Time
		want int
	}{
		{time.Date(2026, 10, 24, 0, 5, 0, 0, creditZone), 0},
		{time.Date(2026, 10, 25, 0, 5, 0, 0, creditZone), 1},
		{time.Date(2026, 10, 31, 23, 59, 0, 0, creditZone), 7},
		{time.Date(2026, 11, 17, 1, 0, 0, 0, creditZone), 24},
	}
	for _, c := range cases {
		if got := daysUntil(base, c.to); got != c.want {
			t.Errorf("daysUntil(%s) = %d, want %d", c.to.Format("2006-01-02"), got, c.want)
		}
	}
}
