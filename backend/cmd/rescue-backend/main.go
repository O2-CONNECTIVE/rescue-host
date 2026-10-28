// rescue-backend は未配線の起動入口であり、製品の制御受付を開始しない。
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "製品の制御起動は未接続です")
	os.Exit(2)
}
