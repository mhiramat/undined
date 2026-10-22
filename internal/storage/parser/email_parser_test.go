package parser

import (
	"strings"
	"testing"
)

func TestParseEmail(t *testing.T) {
	raw := `From: "Kernel Developer" <dev@example.com>
To: linux-trace-kernel@vger.kernel.org
Cc: Steven Rostedt <rostedt@goodmis.org>
Subject: [PATCH] ftrace: fix kprobe event handling
Date: Sat, 03 Oct 2026 14:10:05 +0200
Message-ID: <20261003141005.12345-1-dev@example.com>
In-Reply-To: <cover.20261003.ftrace@kernel.org>
References: <root.20261003@kernel.org> <cover.20261003.ftrace@kernel.org>
Content-Type: text/plain; charset=utf-8

This patch fixes a corner case in kprobe event handling.

Signed-off-by: Kernel Developer <dev@example.com>
---
diff --git a/kernel/trace/trace_kprobe.c b/kernel/trace/trace_kprobe.c
--- a/kernel/trace/trace_kprobe.c
+++ b/kernel/trace/trace_kprobe.c
@@ -100,6 +100,7 @@ static int trace_kprobe_create(...)
+	/* check return value */
`

	parsed, err := ParseEmail([]byte(raw))
	if err != nil {
		t.Fatalf("ParseEmail failed: %v", err)
	}

	if parsed.MessageID != "20261003141005.12345-1-dev@example.com" {
		t.Errorf("unexpected message_id: %s", parsed.MessageID)
	}
	if parsed.FromName != "Kernel Developer" {
		t.Errorf("unexpected from_name: %s", parsed.FromName)
	}
	if parsed.FromAddr != "dev@example.com" {
		t.Errorf("unexpected from_addr: %s", parsed.FromAddr)
	}
	if parsed.Subject != "[PATCH] ftrace: fix kprobe event handling" {
		t.Errorf("unexpected subject: %s", parsed.Subject)
	}
	if parsed.EpochTimestamp == 0 {
		t.Errorf("epoch timestamp should not be 0")
	}
	if !parsed.IsPatch {
		t.Errorf("expected is_patch to be true")
	}
	if !strings.Contains(parsed.BodyPlain, "diff --git") {
		t.Errorf("expected body_plain to contain patch")
	}
}

func TestExtractMsgIDs(t *testing.T) {
	header := "<id1@kernel.org> <id2@kernel.org>"
	ids := ExtractMsgIDs(header)
	if len(ids) != 2 || ids[0] != "id1@kernel.org" || ids[1] != "id2@kernel.org" {
		t.Errorf("unexpected ids: %v", ids)
	}
}

func TestBuildReplyRecipients(t *testing.T) {
	to, cc := BuildReplyRecipients(
		"author@kernel.org",
		"dev@example.com, list@vger.kernel.org",
		"reviewer@kernel.org",
		"dev@example.com",
	)

	if to != "author@kernel.org" {
		t.Errorf("expected to=author@kernel.org, got %s", to)
	}
	// dev@example.com should be excluded from Cc, list and reviewer included
	if strings.Contains(cc, "dev@example.com") {
		t.Errorf("Cc should exclude self email: %s", cc)
	}
	if !strings.Contains(cc, "list@vger.kernel.org") {
		t.Errorf("Cc should include list: %s", cc)
	}
	if !strings.Contains(cc, "reviewer@kernel.org") {
		t.Errorf("Cc should include reviewer: %s", cc)
	}
}

func TestExtractAddresses(t *testing.T) {
	to := `"Kernel Developer" <dev@example.com>, linux-trace-kernel@vger.kernel.org`
	cc := `Reviewer <reviewer@kernel.org>, dev@example.com; <extra@kernel.org>`

	addrs := ExtractAddresses(to, cc)

	if len(addrs) != 4 {
		t.Fatalf("expected 4 unique addresses, got %d: %+v", len(addrs), addrs)
	}

	expected := map[string]string{
		"dev@example.com":                  "Kernel Developer",
		"linux-trace-kernel@vger.kernel.org": "",
		"reviewer@kernel.org":              "Reviewer",
		"extra@kernel.org":                 "",
	}

	for _, a := range addrs {
		expName, ok := expected[a.Address]
		if !ok {
			t.Errorf("unexpected address: %s", a.Address)
			continue
		}
		if a.Name != expName {
			t.Errorf("expected name %q for %s, got %q", expName, a.Address, a.Name)
		}
	}
}

func TestExtractAttachments(t *testing.T) {
	raw := `From: "Kernel Developer" <dev@example.com>
To: linux-trace-kernel@vger.kernel.org
Subject: Test with multiple attachments
Date: Fri, 24 Oct 2026 10:00:00 +0000
Message-ID: <test-attachments@example.com>
MIME-Version: 1.0
Content-Type: multipart/mixed; boundary="BOUNDARY_TEST"

--BOUNDARY_TEST
Content-Type: text/plain; charset=utf-8

Please find the attached patches and logs.

--BOUNDARY_TEST
Content-Type: text/x-diff; name="0001-fix.patch"
Content-Disposition: attachment; filename="0001-fix.patch"
Content-Transfer-Encoding: base64

ZGlmZiAtLWdpdCBhL2Zvby5jIGIvZm9vLmMKLS0tIGEvZm9vLmMKKysrIGIvZm9vLmMKQEAg
LTEgKzEgQEAKLWZvbwoKK2Jhcgo=

--BOUNDARY_TEST
Content-Type: text/plain; name="syslog.txt"
Content-Disposition: attachment; filename*=utf-8''syslog%20report.txt
Content-Transfer-Encoding: quoted-printable

Error in module X=0D=0AStatus: Failed=0D=0A

--BOUNDARY_TEST
Content-Type: application/octet-stream
Content-Disposition: attachment; filename="../../dangerous/path/exploit.bin"
Content-Transfer-Encoding: base64

AQIDBAU=

--BOUNDARY_TEST
Content-Type: image/png; name="diagram.png"
Content-Disposition: inline; filename="diagram.png"
Content-Transfer-Encoding: base64

iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==

--BOUNDARY_TEST--
`

	attachments, err := ExtractAttachments([]byte(raw))
	if err != nil {
		t.Fatalf("ExtractAttachments failed: %v", err)
	}

	if len(attachments) != 4 {
		t.Fatalf("expected 4 attachments, got %d", len(attachments))
	}

	// 1. Base64 patch
	if attachments[0].FileName != "0001-fix.patch" {
		t.Errorf("expected 0001-fix.patch, got %s", attachments[0].FileName)
	}
	if attachments[0].ContentType != "text/x-diff" {
		t.Errorf("expected text/x-diff, got %s", attachments[0].ContentType)
	}
	if !strings.Contains(string(attachments[0].Payload), "diff --git a/foo.c") {
		t.Errorf("expected decoded diff content in payload: %s", string(attachments[0].Payload))
	}

	// 2. Quoted-printable with RFC 2231 encoded filename
	if attachments[1].FileName != "syslog report.txt" {
		t.Errorf("expected 'syslog report.txt', got %s", attachments[1].FileName)
	}
	if !strings.Contains(string(attachments[1].Payload), "Status: Failed") {
		t.Errorf("expected decoded syslog content: %s", string(attachments[1].Payload))
	}

	// 3. Path traversal filename sanitization
	if attachments[2].FileName != "exploit.bin" {
		t.Errorf("expected path traversal to be stripped to 'exploit.bin', got %s", attachments[2].FileName)
	}

	// 4. Inline diagram with filename
	if attachments[3].FileName != "diagram.png" {
		t.Errorf("expected 'diagram.png', got %s", attachments[3].FileName)
	}
}

func TestSanitizeAttachmentFileName(t *testing.T) {
	tests := []struct {
		input       string
		index       int
		contentType string
		expected    string
	}{
		{"../../etc/passwd", 0, "text/plain", "passwd"},
		{`"quoted.patch"`, 1, "text/x-diff", "quoted.patch"},
		{"\r\nunsafe\n.tar.gz", 2, "application/gzip", "unsafe.tar.gz"},
		{"", 0, "text/x-diff", "attachment-1.patch"},
		{"   ", 1, "application/pdf", "attachment-2.pdf"},
		{"", 2, "application/octet-stream", "attachment-3.bin"},
	}

	for _, tc := range tests {
		got := SanitizeAttachmentFileName(tc.input, tc.index, tc.contentType)
		if got != tc.expected {
			t.Errorf("SanitizeAttachmentFileName(%q, %d, %q) = %q, expected %q",
				tc.input, tc.index, tc.contentType, got, tc.expected)
		}
	}
}

