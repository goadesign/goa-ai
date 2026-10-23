// Package runtime measures the same synthetic complete service through raw and
// compiled binding. Compilation and retained snapshots are measured separately
// from activity binding, which always restores its own generated contracts.
package runtime

import (
	"fmt"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
	genregistry "goa.design/goa-ai/registry/gen/registry"
)

func BenchmarkCompileStaticService(b *testing.B) {
	fixture := newStaticBindingFixture(b, 46)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		compiled, err := CompileStaticService(fixture.expected, fixture.selected)
		if err != nil {
			b.Fatal(err)
		}
		runtime.KeepAlive(compiled)
	}
	runtime.KeepAlive(fixture)
}

func BenchmarkCompiledStaticServiceBinding(b *testing.B) {
	for _, consumer := range []string{"sparse", "full"} {
		b.Run(consumer, func(b *testing.B) {
			fixture := newStaticBindingFixture(b, 46)
			names := fixture.namesFor(consumer)
			compiled, err := CompileStaticService(fixture.expected, fixture.selected)
			require.NoError(b, err)
			require.NoError(b, fixture.catalog().BindCompiledStaticService(testRegistryName, compiled, readTestStaticResource, names...))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				catalog := fixture.catalog()
				if err := catalog.BindCompiledStaticService(testRegistryName, compiled, readTestStaticResource, names...); err != nil {
					b.Fatal(err)
				}
				runtime.KeepAlive(catalog)
			}
			runtime.KeepAlive(compiled)
			runtime.KeepAlive(fixture)
		})
	}
}

func BenchmarkStaticServiceSnapshotRetained(b *testing.B) {
	for _, revisions := range []int{1, 8} {
		for _, representation := range []string{"raw", "compiled"} {
			b.Run(fmt.Sprintf("%s/revisions_%d", representation, revisions), func(b *testing.B) {
				fixture := newStaticBindingFixture(b, 46)
				warm, err := CompileStaticService(fixture.expected, fixture.selected)
				require.NoError(b, err)
				runtime.KeepAlive(warm)
				warm = nil
				var totalLive uint64
				var totalEncoded int
				b.ResetTimer()
				for range b.N {
					raw := make([]*genregistry.ResolvedToolset, revisions)
					compiled := make([]*CompiledStaticService, revisions)
					runtime.GC()
					runtime.GC()
					var before, after runtime.MemStats
					runtime.ReadMemStats(&before)
					for index := range revisions {
						fixture.selected.RegistrationToken = fmt.Sprintf("%064x", index+1)
						if representation == "raw" {
							encoded, err := genregistry.EncodeResolvedToolset(fixture.selected)
							require.NoError(b, err)
							raw[index], err = genregistry.DecodeResolvedToolset(encoded)
							require.NoError(b, err)
							totalEncoded += len(encoded)
						} else {
							var err error
							compiled[index], err = CompileStaticService(fixture.expected, fixture.selected)
							require.NoError(b, err)
							for _, encoded := range compiled[index].selections {
								totalEncoded += len(encoded)
							}
						}
					}
					runtime.GC()
					runtime.GC()
					runtime.ReadMemStats(&after)
					if after.HeapAlloc < before.HeapAlloc {
						b.Fatal("retained heap decreased while checked snapshots remained live")
					}
					totalLive += after.HeapAlloc - before.HeapAlloc
					runtime.KeepAlive(raw)
					runtime.KeepAlive(compiled)
					runtime.KeepAlive(fixture)
				}
				b.ReportMetric(float64(totalLive)/float64(b.N*revisions), "retained_B/revision")
				b.ReportMetric(float64(totalEncoded)/float64(b.N*revisions), "encoded_B/revision")
			})
		}
	}
}
