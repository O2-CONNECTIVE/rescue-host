package servicehost

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/protectedkey"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"
)

func controlFactoryConfig(t *testing.T) serviceready.Config {
	t.Helper()
	base, _ := nativeConfigFixture(t)
	return serviceready.Config{
		Profile: base.Profile, PrimaryToken: base.PrimaryToken,
		Endpoint:           `\\.\pipe\rescue-service-` + base.Profile.ServiceName,
		ServiceCommandLine: `"` + base.Profile.SupervisorImage.Path + `"`,
		ServiceSecurity:    serviceready.SecurityPolicy{OwnerSID: base.Profile.Keys.ProcessUserSID, Protected: true, ACL: []protectedkey.ACE{{SID: base.Profile.Keys.ServiceSID, Mask: 0xf01ff}}},
		ProcessSecurity:    serviceready.SecurityPolicy{OwnerSID: base.Profile.Keys.ProcessUserSID, Protected: true, ACL: []protectedkey.ACE{{SID: base.Profile.Keys.ServiceSID, Mask: 0x1fffff}}},
		CleanupTimeout:     time.Second,
	}
}

func TestServiceNativeFactoriesOwnsOneSnapshotBeforeBothConstructors(t *testing.T) {
	c := controlFactoryConfig(t)
	want := controlFactoryConfig(t)
	var readyInput serviceready.Config
	var authInput bootstrapauth.Profile
	readyCalls, authCalls := 0, 0
	pair, err := buildServiceNativeFactories(c, serviceNativeConstructors{
		ready: func(got serviceready.Config) (*serviceready.Factory, error) {
			readyCalls++
			readyInput = got
			factory, err := serviceready.NewNative(got)
			// Mutate the original caller between the two real static constructors.
			c.Profile.SupervisorImage.Nodes[0].ACL[0].SID = "caller mutation"
			c.Profile.ChildImage.Nodes[1].Path = `D:\changed`
			key := c.Profile.Keys.Catalog["host_key"]
			key.Nodes[0].OwnerSID = "caller mutation"
			delete(c.Profile.Keys.Catalog, "host_key")
			c.ServiceSecurity.ACL[0].Mask = 0
			c.ProcessSecurity.ACL[0].Mask = 0
			return factory, err
		},
		auth: func(got bootstrapauth.Profile) (*bootstrapauth.Factory, error) {
			authCalls++
			authInput = got
			return bootstrapauth.NewNativeFactory(got)
		},
	})
	if err != nil || pair == nil || readyCalls != 1 || authCalls != 1 {
		t.Fatal("caller変更から独立した同じsnapshotを両実Ctorへ渡しませんでした", err)
	}
	c.Profile.ChildImage.Nodes[0].ACL[0].Mask = 0
	if !reflect.DeepEqual(readyInput, want) || !reflect.DeepEqual(authInput, want.Profile) {
		t.Fatal("元callerのmap/slice変更が構成snapshotへ漏れました")
	}
}

func TestServiceNativeFactoriesBuildsRealStaticPair(t *testing.T) {
	pair, err := newServiceNativeFactories(controlFactoryConfig(t))
	if err != nil || pair == nil || pair.ready == nil || pair.auth == nil {
		t.Fatal("同じ保護設定から実Factory pairを構成しませんでした", err)
	}
	// Invalid contexts stop both concrete wrappers before platform/native entry.
	if witness, err := pair.prepareWitness(nil); witness != nil || !errors.Is(err, serviceready.ErrDenied) {
		t.Fatal("無効ctxからSCM witnessを取得しました", err)
	}
	if endpoint, err := pair.prepareEndpoint(context.Background()); endpoint != nil || !errors.Is(err, serviceready.ErrDenied) {
		t.Fatal("無期限ctxから物理endpointを取得しました", err)
	}
	if invalid, err := newServiceNativeFactories(serviceready.Config{}); invalid != nil || !errors.Is(err, ErrProvisioningRejected) {
		t.Fatal("不完全な保護設定をpairへ昇格しました", err)
	}
}

func TestServiceNativeFactoriesRejectsPartialPair(t *testing.T) {
	for _, stage := range []string{"nil ready", "failed ready", "nil auth", "failed auth", "mixed endpoint"} {
		t.Run(stage, func(t *testing.T) {
			c := controlFactoryConfig(t)
			if stage == "mixed endpoint" {
				c.Endpoint += "-other"
			}
			authCalls := 0
			pair, err := buildServiceNativeFactories(c, serviceNativeConstructors{
				ready: func(got serviceready.Config) (*serviceready.Factory, error) {
					if stage == "nil ready" {
						return nil, nil
					}
					if stage == "failed ready" {
						return nil, serviceready.ErrConfig
					}
					return serviceready.NewNative(got)
				},
				auth: func(got bootstrapauth.Profile) (*bootstrapauth.Factory, error) {
					authCalls++
					if stage == "nil auth" {
						return nil, nil
					}
					if stage == "failed auth" {
						return nil, bootstrapauth.ErrConfig
					}
					return bootstrapauth.NewNativeFactory(got)
				},
			})
			if pair != nil || !errors.Is(err, ErrProvisioningRejected) {
				t.Fatal("部分構成を完成pairにしました", err)
			}
			wantAuth := 0
			if stage == "nil auth" || stage == "failed auth" {
				wantAuth = 1
			}
			if authCalls != wantAuth {
				t.Fatal("欠落/失敗した構成から別Factoryへfallbackしました", authCalls)
			}
		})
	}
}

func TestServiceNativeFactoriesRejectsZeroCopiedAndMixedWrappers(t *testing.T) {
	first, err := newServiceNativeFactories(controlFactoryConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	second, err := newServiceNativeFactories(controlFactoryConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	copy := *first
	mixed := &serviceNativeFactories{ready: first.ready, auth: second.auth}
	for _, pair := range []*serviceNativeFactories{nil, new(serviceNativeFactories), &copy, mixed} {
		if pair.canonical() {
			t.Fatal("未構成/コピー/外来memberのpairを受理しました")
		}
		if w, err := pair.prepareWitness(nil); w != nil || !errors.Is(err, ErrProvisioningUnconfigured) {
			t.Fatal("不正pairからwitnessへ進みました", err)
		}
		if e, err := pair.prepareEndpoint(nil); e != nil || !errors.Is(err, ErrProvisioningUnconfigured) {
			t.Fatal("不正pairからendpointへ進みました", err)
		}
	}
	if !first.canonical() || !second.canonical() {
		t.Fatal("コピーの拒否で元pairを変更しました")
	}
}

func TestServiceNativeFactoriesRejectsMissingConstructorSeam(t *testing.T) {
	for _, constructors := range []serviceNativeConstructors{
		{}, {ready: serviceready.NewNative}, {auth: bootstrapauth.NewNativeFactory},
	} {
		if p, err := buildServiceNativeFactories(controlFactoryConfig(t), constructors); p != nil || !errors.Is(err, ErrProvisioningUnconfigured) {
			t.Fatal("未構成のprivate seamを実行しました", err)
		}
	}
}

func TestServiceNativeSnapshotMutableFieldInventory(t *testing.T) {
	var paths []string
	var walk func(reflect.Type, string)
	walk = func(typ reflect.Type, path string) {
		if typ == reflect.TypeOf(time.Time{}) {
			return
		} // Immutable value; Location is not caller-editable.
		switch typ.Kind() {
		case reflect.Struct:
			for i := 0; i < typ.NumField(); i++ {
				field := typ.Field(i)
				name := field.Name
				if path != "" {
					name = path + "." + name
				}
				walk(field.Type, name)
			}
		case reflect.Slice:
			paths = append(paths, path)
			walk(typ.Elem(), path+"[]")
		case reflect.Map:
			paths = append(paths, path)
			walk(typ.Elem(), path+"{}")
		case reflect.Array:
			walk(typ.Elem(), path+"[]")
		case reflect.Pointer, reflect.Chan, reflect.Func, reflect.Interface, reflect.UnsafePointer:
			paths = append(paths, path)
		}
	}
	walk(reflect.TypeOf(serviceready.Config{}), "")
	want := []string{
		"ServiceSecurity.ACL", "ProcessSecurity.ACL",
		"Profile.ChildImage.Nodes", "Profile.ChildImage.Nodes[].ACL",
		"Profile.SupervisorImage.Nodes", "Profile.SupervisorImage.Nodes[].ACL",
		"Profile.Keys.Catalog", "Profile.Keys.Catalog{}.Nodes", "Profile.Keys.Catalog{}.Nodes[].ACL",
	}
	sort.Strings(paths)
	sort.Strings(want)
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("設定の可変fieldが変わりました。typed snapshotの所有範囲を再確認してください: %v", paths)
	}
}
