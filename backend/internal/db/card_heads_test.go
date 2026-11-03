package db_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
)

func headByID(t *testing.T, provider, id string) db.CardHead {
	t.Helper()
	h, err := db.GetCardHead(provider, id)
	if err != nil {
		t.Fatalf("GetCardHead(%s,%s): %v", provider, id, err)
	}
	return h
}

func TestMigrateCardHeadsIdempotent(t *testing.T) {
	dbtest.Open(t)
	for range 2 {
		if err := db.MigrateCardHeads(); err != nil {
			t.Fatal(err)
		}
	}
}

// 再次同步只更新产品信息，标注保留；接口不再返回的标 listed=false，不删除。
func TestSyncCardHeadsKeepsNotes(t *testing.T) {
	dbtest.Open(t)
	first := []db.CardHeadProduct{
		{ProductID: "A", Name: "美卡-512998", CardHeads: []string{"512998"}, Network: "MASTER", Country: "US", Currency: "USD"},
		{ProductID: "B", Name: "美卡-428820", CardHeads: []string{"428820"}, Network: "VISA", Country: "US", Currency: "USD"},
	}
	if err := db.SyncCardHeads("p1", first, 100, false); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateCardHeadNotes("p1", "A", "yes", "", "x", "admin", 110); err != nil {
		t.Fatal(err)
	}
	second := []db.CardHeadProduct{
		{ProductID: "A", Name: "美卡-512998-新名", CardHeads: []string{"512998"}, Network: "MASTER", Country: "US", Currency: "USD"},
	}
	if err := db.SyncCardHeads("p1", second, 200, false); err != nil {
		t.Fatal(err)
	}

	a := headByID(t, "p1", "A")
	if a.Name != "美卡-512998-新名" || a.ThreeDS != "yes" || a.Note != "x" || !a.Listed || a.Source != db.CardHeadSourceAPI || a.SyncedAt != 200 {
		t.Fatalf("A = %+v", a)
	}
	if !reflect.DeepEqual(a.CardHeads, []string{"512998"}) {
		t.Fatalf("A.CardHeads = %v", a.CardHeads)
	}
	b := headByID(t, "p1", "B")
	if b.Listed {
		t.Fatalf("B 应标为接口已不再返回: %+v", b)
	}

	list, err := db.ListCardHeads("p1")
	if err != nil || len(list) != 2 {
		t.Fatalf("len=%d err=%v", len(list), err)
	}
	if list[0].ProductID != "A" || list[1].ProductID != "B" {
		t.Fatalf("排序应 listed 在前: %s, %s", list[0].ProductID, list[1].ProductID)
	}
}

// 手动添加的记录被接口返回后转成 api 来源，标注保留。
func TestSyncCardHeadsAdoptsManual(t *testing.T) {
	dbtest.Open(t)
	manual := db.CardHead{Provider: "p1", ProductID: "C", Name: "手动卡", CardHeads: []string{"411111"}, ApplePay: "no", Note: "手动"}
	if err := db.SaveManualCardHead(manual, "admin", 50); err != nil {
		t.Fatal(err)
	}
	if err := db.SyncCardHeads("p1", []db.CardHeadProduct{{ProductID: "C", Name: "接口卡", CardHeads: []string{"411112"}}}, 60, false); err != nil {
		t.Fatal(err)
	}
	c := headByID(t, "p1", "C")
	if c.Source != db.CardHeadSourceAPI || c.Name != "接口卡" || c.ApplePay != "no" || c.Note != "手动" || !c.Listed {
		t.Fatalf("C = %+v", c)
	}
}

// 同步一家服务商，不影响另一家的记录。
func TestSyncCardHeadsOtherProvider(t *testing.T) {
	dbtest.Open(t)
	if err := db.SyncCardHeads("p2", []db.CardHeadProduct{{ProductID: "X", Name: "p2 卡"}}, 10, false); err != nil {
		t.Fatal(err)
	}
	if err := db.SyncCardHeads("p1", []db.CardHeadProduct{{ProductID: "Y", Name: "p1 卡"}}, 20, false); err != nil {
		t.Fatal(err)
	}
	x := headByID(t, "p2", "X")
	if !x.Listed || x.SyncedAt != 10 {
		t.Fatalf("p2 的记录被改动: %+v", x)
	}
	if x.CardHeads == nil {
		t.Fatal("CardHeads 不应为 nil")
	}
}

func TestManualCardHeadRules(t *testing.T) {
	dbtest.Open(t)
	if err := db.SyncCardHeads("p1", []db.CardHeadProduct{{ProductID: "A", Name: "接口卡"}}, 10, false); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveManualCardHead(db.CardHead{Provider: "p1", ProductID: "A", Name: "改名"}, "admin", 20); !errors.Is(err, db.ErrCardHeadNotManual) {
		t.Fatalf("对 api 行手动保存应报 ErrCardHeadNotManual，得到 %v", err)
	}
	if err := db.DeleteManualCardHead("p1", "A"); !errors.Is(err, db.ErrCardHeadNotManual) {
		t.Fatalf("删除 api 行应报 ErrCardHeadNotManual，得到 %v", err)
	}
	if err := db.DeleteManualCardHead("p1", "nope"); !errors.Is(err, db.ErrCardHeadNotFound) {
		t.Fatalf("删除不存在的应报 ErrCardHeadNotFound，得到 %v", err)
	}
	if err := db.UpdateCardHeadNotes("p1", "nope", "", "", "", "admin", 30); !errors.Is(err, db.ErrCardHeadNotFound) {
		t.Fatalf("标注不存在的应报 ErrCardHeadNotFound，得到 %v", err)
	}
	if _, err := db.GetCardHead("p1", "nope"); !errors.Is(err, db.ErrCardHeadNotFound) {
		t.Fatalf("GetCardHead 不存在应报 ErrCardHeadNotFound，得到 %v", err)
	}

	m := db.CardHead{Provider: "p1", ProductID: "M", Name: "手动", CardHeads: []string{"411111"}, ThreeDS: "yes"}
	if err := db.SaveManualCardHead(m, "admin", 40); err != nil {
		t.Fatal(err)
	}
	m.Name = "手动改名"
	if err := db.SaveManualCardHead(m, "admin2", 50); err != nil {
		t.Fatal(err)
	}
	got := headByID(t, "p1", "M")
	if got.Source != db.CardHeadSourceManual || !got.Listed || got.Name != "手动改名" || got.UpdatedBy != "admin2" || got.UpdatedAt != 50 {
		t.Fatalf("M = %+v", got)
	}
	if err := db.DeleteManualCardHead("p1", "M"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetCardHead("p1", "M"); !errors.Is(err, db.ErrCardHeadNotFound) {
		t.Fatalf("删除后应不存在，得到 %v", err)
	}
}

// pruneMissing 为真时，接口不再返回的 api 行直接删除；手动行不受影响。
func TestSyncCardHeadsPruneMissing(t *testing.T) {
	dbtest.Open(t)
	if err := db.SyncCardHeads("p1", []db.CardHeadProduct{{ProductID: "A"}, {ProductID: "B"}}, 10, true); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveManualCardHead(db.CardHead{Provider: "p1", ProductID: "M", Name: "手动"}, "admin", 11); err != nil {
		t.Fatal(err)
	}
	if err := db.SyncCardHeads("p1", []db.CardHeadProduct{{ProductID: "A"}}, 20, true); err != nil {
		t.Fatal(err)
	}
	list, err := db.ListCardHeads("p1")
	if err != nil || len(list) != 2 || list[0].ProductID != "A" || list[1].ProductID != "M" {
		t.Fatalf("list = %+v err=%v", list, err)
	}
}
