package backend

import (
	"reflect"
	"testing"
)

// 試験宛先、dial、時計、gate解除が公開設定へ紛れないことをAPI境界で検査する。
func TestAFKUnit5PublicConfigCannotSelectFixture(t *testing.T) {
	allowed := map[string]bool{"Identity": true, "OperationMode": true, "AllowedKeys": true, "HostKey": true, "MaximumDuration": true, "LeaseDuration": true, "MaxChannels": true, "MaxHandshakes": true, "MaxTransports": true}
	typ := reflect.TypeOf(Config{})
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if !allowed[field.Name] {
			t.Fatalf("未審査の公開設定項目: %s", field.Name)
		}
		if field.Type.Kind() == reflect.Func {
			t.Fatal("公開設定で実装差替え")
		}
	}
}
