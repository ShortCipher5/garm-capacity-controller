package managedgarm

import (
	"fmt"
	"net"
	"strings"
	"testing"
)

func TestStockReadHeldListener(t *testing.T) {
	// Local disposable socket only. No HTTP, namespace transition, or Docker.
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9445})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	held, err := retainStockReadListener(listener)
	if err != nil {
		t.Fatal(err)
	}
	header := "sl local_address rem_address st\n"
	rows := header
	for i, p := range []int{8443, 9443, 9444, 9998} {
		rows += fmt.Sprintf("%d: 0100007F:%04X 00000000:0000 0A\n", i, p)
	}
	row := fmt.Sprintf("4: 0100007F:24E5 00000000:0000 0A 0:0 0:0 0 0 0 %d\n", held.inode)
	tables := [2][]byte{[]byte(rows + row), []byte(header)}
	if !stockReadListenerSet(tables, held) {
		t.Fatal("held accepting identity rejected")
	}
	if stockReadListenerSet(tables, nil) {
		t.Fatal("unowned fifth port accepted")
	}
	for _, bad := range []string{rows, rows + row + row, rows + strings.Replace(row, fmt.Sprint(held.inode), "0", 1), rows + strings.Replace(row, fmt.Sprint(held.inode), "invalid", 1), rows + strings.Replace(row, "0100007F", "00000000", 1), rows + "4: 0100007F:24E5 0:0 0A\n"} {
		if stockReadListenerSet([2][]byte{[]byte(bad), []byte(header)}, held) {
			t.Fatal("unknown/malformed identity accepted")
		}
	}
	held.inode++
	if stockReadListenerSet(tables, held) {
		t.Fatal("identity drift accepted")
	}
	held.inode--
	listener.Close()
	if stockReadListenerSet(tables, held) {
		t.Fatal("closed identity accepted")
	}
}
func TestStockReadForeignListener(t *testing.T) {
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if _, err := retainStockReadListener(listener); err == nil {
		t.Fatal("foreign port accepted")
	}
	if _, err := retainStockReadListener(nil); err == nil {
		t.Fatal("nil accepted")
	}
}
