package langcap

import "testing"

// TestFamilyUnions 族并集派生面的不变式：FuncDiff ⊆ GraphScan（词法 diff 参与
// 文件必须能进图层扫描）；PolySymbol ⊆ Py∪JSTS（textx 上游 scanPolyFiles 覆盖面）；
// 各面无重复扩展名。
func TestFamilyUnions(t *testing.T) {
	for _, e := range FuncDiffExts {
		if !HasExt(GraphScanExts, e) {
			t.Fatalf("FuncDiffExts 的 %q 不在 GraphScanExts", e)
		}
	}
	pyjs := concat(PyExts, JSTSExts)
	for _, e := range PolySymbolExts {
		if !HasExt(pyjs, e) {
			t.Fatalf("PolySymbolExts 的 %q 不在 Py/JSTS 族", e)
		}
	}
	for name, set := range map[string][]string{
		"GraphScan": GraphScanExts, "FuncDiff": FuncDiffExts,
		"SemChunk": SemChunkExts, "PolySymbol": PolySymbolExts,
	} {
		seen := map[string]bool{}
		for _, e := range set {
			if seen[e] {
				t.Fatalf("%s 面扩展名重复: %s", name, e)
			}
			seen[e] = true
		}
	}
}

// TestLangsOfRegistered 全部面扩展名都登记了展示名——LangsOf 未登记时透出裸
// 扩展名，描述面会失真；新扩展名入集必须同步 extLang。
func TestLangsOfRegistered(t *testing.T) {
	for _, set := range [][]string{GraphScanExts, FuncDiffExts, SemChunkExts, PolySymbolExts} {
		for _, e := range set {
			if _, ok := extLang[e]; !ok {
				t.Fatalf("extLang 未登记 %q（LangsOf 会把它当语言名透出）", e)
			}
		}
	}
}
