package auth

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestRemoveFilePreventsPendingSaves(t *testing.T) {
	fp := filepath.Join(t.TempDir(), "workbuddy-u1.json")
	a := &Auth{UID: "u1", AccessToken: "at", FilePath: fp}
	if err := a.SaveAtomic(); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				if err := a.SaveAtomic(); err != nil {
					t.Errorf("pending save: %v", err)
				}
			}
		}()
	}
	if err := a.RemoveFile(); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if _, err := os.Stat(fp); !os.IsNotExist(err) {
		t.Fatalf("removed credential recreated: %v", err)
	}
	// 重新导入使用新对象；旧对象的延迟保存或重复删除不能破坏新凭证。
	reimported := &Auth{UID: "u1", AccessToken: "new-at", FilePath: fp}
	if err := reimported.SaveAtomic(); err != nil {
		t.Fatal(err)
	}
	if err := a.SaveAtomic(); err != nil {
		t.Fatal(err)
	}
	if err := a.RemoveFile(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(fp)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Parse(raw)
	if err != nil || got.AccessToken != "new-at" {
		t.Fatalf("reimported credential changed: %s, %v", raw, err)
	}
}

func TestRemoveFileFailureCanRetry(t *testing.T) {
	fp := filepath.Join(t.TempDir(), "workbuddy-u1.json")
	if err := os.Mkdir(fp, 0700); err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(fp, "blocker")
	if err := os.WriteFile(blocker, nil, 0600); err != nil {
		t.Fatal(err)
	}
	a := &Auth{UID: "u1", AccessToken: "at", FilePath: fp}
	if err := a.RemoveFile(); err == nil {
		t.Fatal("removing a nonempty directory must fail")
	}
	if a.removed {
		t.Fatal("failed deletion must not suppress future saves")
	}
	if err := os.RemoveAll(fp); err != nil {
		t.Fatal(err)
	}
	if err := a.SaveAtomic(); err != nil {
		t.Fatal(err)
	}
	if err := a.RemoveFile(); err != nil {
		t.Fatal(err)
	}
	if err := a.SaveAtomic(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(fp); !os.IsNotExist(err) {
		t.Fatalf("retry left credential on disk: %v", err)
	}
}
