package upstream

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// TestResourceBatchesUseDeductionEndTime 钉住「两套口径并存且允许不相等」。
//
// 路由口径（Expiring/EarliestAt/EarliestRemaining）继续读 CycleEndTime，展示口径
// （Batches）读 packageExpiry（DeductionEndTime 优先）。本用例构造一个 CycleEndTime
// 与 DeductionEndTime 明显不同的包，断言两者给出不同时刻——这是防止后人「好心去重」
// 把两套字段合并、静默改变选号行为的唯一护栏。
func TestResourceBatchesUseDeductionEndTime(t *testing.T) {
	cycle := time.Now().In(softRateResetLoc).Add(30 * 24 * time.Hour).Truncate(time.Second)
	deduct := time.Now().Add(72 * time.Hour).Truncate(time.Second)
	c := testClient(func(r *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(r.URL.Path, "/v2/billing/meter/get-user-resource") {
			return nil, errors.New("wrong path: " + r.URL.Path)
		}
		return jsonResp(200, `{"code":0,"data":{"Response":{"Data":{"Accounts":[`+
			`{"PackageName":"gift","CycleCapacitySize":100,"CycleCapacityRemain":60,"CycleCapacityUsed":40,`+
			`"CycleEndTime":"`+cycle.Format(packageEndLayout)+`",`+
			`"DeductionEndTime":`+strconv.FormatInt(deduct.UnixMilli(), 10)+`}`+
			`]}}}}`), nil
	})

	snap, err := c.UserResourceDetailedWithExpiry(&auth.Auth{AccessToken: "at", UID: "u1"}, 0)
	if err != nil {
		t.Fatalf("resource: %v", err)
	}
	if snap.Remain != 60 {
		t.Fatalf("remain=%d want 60", snap.Remain)
	}
	// 路由口径：CycleEndTime（行为不变）。
	if !snap.EarliestAt.Equal(cycle) || snap.EarliestRemaining != 60 {
		t.Fatalf("routing expiry=%v/%d want %v/60", snap.EarliestAt, snap.EarliestRemaining, cycle)
	}
	// 展示口径：DeductionEndTime（真失效时刻）。
	if len(snap.Batches) != 1 || !snap.Batches[0].At.Equal(deduct) || snap.Batches[0].Remain != 60 {
		t.Fatalf("batches=%+v want one batch at %v/60", snap.Batches, deduct)
	}
	if snap.Batches[0].At.Equal(snap.EarliestAt) {
		t.Fatal("两套口径必须允许不同：合并它们会静默改变 prefer_expiring 行为")
	}
}

// TestResourceBatchesMergeSameInstant 同一到期时刻的多个包合并成一批（与
// EarliestRemaining 的「同一时刻求和」语义一致），且只统计未来 + 正余额。
func TestResourceBatchesMergeSameInstant(t *testing.T) {
	soon := time.Now().Add(48 * time.Hour).Truncate(time.Second)
	past := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	ms := func(v int64) string { return strconv.FormatInt(v, 10) }
	c := testClient(func(r *http.Request) (*http.Response, error) {
		return jsonResp(200, `{"code":0,"data":{"Response":{"Data":{"Accounts":[`+
			`{"PackageName":"a","CycleCapacitySize":10,"CycleCapacityRemain":10,"CycleCapacityUsed":0,"DeductionEndTime":`+ms(soon.UnixMilli())+`},`+
			`{"PackageName":"b","CycleCapacitySize":20,"CycleCapacityRemain":20,"CycleCapacityUsed":0,"DeductionEndTime":`+ms(soon.UnixMilli())+`},`+
			`{"PackageName":"expired","CycleCapacitySize":30,"CycleCapacityRemain":30,"CycleCapacityUsed":0,"DeductionEndTime":`+ms(past.UnixMilli())+`},`+
			`{"PackageName":"used-up","CycleCapacitySize":40,"CycleCapacityRemain":0,"CycleCapacityUsed":40,"DeductionEndTime":`+ms(soon.UnixMilli())+`}`+
			`]}}}}`), nil
	})
	snap, err := c.UserResourceDetailedWithExpiry(&auth.Auth{AccessToken: "at", UID: "u1"}, 0)
	if err != nil {
		t.Fatalf("resource: %v", err)
	}
	if len(snap.Batches) != 1 {
		t.Fatalf("batches=%+v want exactly one merged batch（已过期与零余额必须剔除）", snap.Batches)
	}
	if snap.Batches[0].Remain != 30 {
		t.Fatalf("merged remain=%d want 30", snap.Batches[0].Remain)
	}
}
