package main

import (
	"bufio"
	"strings"
	"testing"
)

// -password 给了就直接用，不去碰标准输入（脚本化建号依赖这一点）。
func TestResolvePasswordPrefersFlag(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader(""))
	got, err := resolvePassword(reader, "from-flag")
	if err != nil {
		t.Fatal(err)
	}
	if got != "from-flag" {
		t.Fatalf("got %q", got)
	}
}

// 未给 -password 时从标准输入读两次并要求一致。
func TestResolvePasswordReadsStdinTwice(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("first-secret\r\nfirst-secret\n"))
	got, err := resolvePassword(reader, "")
	if err != nil {
		t.Fatal(err)
	}
	if got != "first-secret" {
		t.Fatalf("got %q，应去掉行尾的 \\r\\n", got)
	}
}

func TestResolvePasswordRejectsMismatch(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("one-secret\ntwo-secret\n"))
	if _, err := resolvePassword(reader, ""); err == nil {
		t.Fatal("两次输入不一致时必须报错")
	}
}

func TestResolvePasswordRejectsEmptyStdin(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader(""))
	if _, err := resolvePassword(reader, ""); err == nil {
		t.Fatal("标准输入为空时必须报错，而不是用空口令建号")
	}
}
