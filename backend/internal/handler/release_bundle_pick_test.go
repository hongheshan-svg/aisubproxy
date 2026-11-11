package handler

import (
	"runtime"
	"testing"
)

// 本机架构对应的发布包文件名（测试在 arm64 的开发机上也要通过）。
var testBundleName = func() string {
	if runtime.GOARCH == "arm64" {
		return "cdk-bundle-linux-arm64.tgz"
	}
	return "cdk-bundle-linux-amd64.tgz"
}()

func ra(name string) releaseAsset { return releaseAsset{Name: name, URL: "https://x/" + name} }

// H1-07：包、.sha256、.sig 三者齐全才算可一键更新；别的架构的包不算。
func TestBundleCompleteNeedsAllThree(t *testing.T) {
	if !bundleComplete([]releaseAsset{ra(testBundleName), ra(testBundleName + ".sha256"), ra(testBundleName + ".sig")}) {
		t.Fatal("三个齐全应为可更新")
	}
	if bundleComplete([]releaseAsset{ra(testBundleName), ra(testBundleName + ".sha256")}) {
		t.Fatal("缺签名不应算可更新")
	}
	if bundleComplete([]releaseAsset{ra(testBundleName), ra(testBundleName + ".sig")}) {
		t.Fatal("缺校验文件不应算可更新")
	}
	if bundleComplete([]releaseAsset{ra(testBundleName + ".sha256"), ra(testBundleName + ".sig")}) {
		t.Fatal("没有包不应算可更新")
	}
	other := "cdk-bundle-linux-arm64.tgz"
	if runtime.GOARCH == "arm64" {
		other = "cdk-bundle-linux-amd64.tgz"
	}
	if bundleComplete([]releaseAsset{ra(other), ra(other + ".sha256"), ra(other + ".sig")}) {
		t.Fatal("别的架构的包不应算可更新")
	}
}
