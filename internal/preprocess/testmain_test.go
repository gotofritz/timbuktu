package preprocess_test

import (
	"bytes"
	"fmt"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	_ = os.MkdirAll("testdata", 0755)
	if _, err := os.Stat("testdata/sample.pdf"); os.IsNotExist(err) {
		_ = os.WriteFile("testdata/sample.pdf", generateMinimalPDF(), 0644)
	}
	os.Exit(m.Run())
}

// generateMinimalPDF returns a minimal valid PDF containing the text "Hello World".
func generateMinimalPDF() []byte {
	var buf bytes.Buffer
	w := func(s string) { buf.WriteString(s) } //nolint:errcheck

	w("%PDF-1.1\n")
	off1 := buf.Len()
	w("1 0 obj\n<</Type /Catalog /Pages 2 0 R>>\nendobj\n")
	off2 := buf.Len()
	w("2 0 obj\n<</Type /Pages /Kids [3 0 R] /Count 1>>\nendobj\n")
	off3 := buf.Len()
	w("3 0 obj\n<</Type /Page /Parent 2 0 R /MediaBox [0 0 200 200]" +
		" /Contents 4 0 R /Resources << /Font << /F1 << /Type /Font" +
		" /Subtype /Type1 /BaseFont /Helvetica >> >> >> >>\nendobj\n")
	off4 := buf.Len()
	stream := "BT /F1 12 Tf 50 150 Td (Hello World) Tj ET\n"
	w(fmt.Sprintf("4 0 obj\n<</Length %d>>\nstream\n", len(stream)))
	w(stream)
	w("endstream\nendobj\n")

	xrefOff := buf.Len()
	w("xref\n0 5\n")
	w(fmt.Sprintf("%010d %05d f \n", 0, 65535))
	w(fmt.Sprintf("%010d %05d n \n", off1, 0))
	w(fmt.Sprintf("%010d %05d n \n", off2, 0))
	w(fmt.Sprintf("%010d %05d n \n", off3, 0))
	w(fmt.Sprintf("%010d %05d n \n", off4, 0))
	w(fmt.Sprintf("trailer\n<</Size 5 /Root 1 0 R>>\nstartxref\n%d\n%%%%EOF\n", xrefOff))

	return buf.Bytes()
}
