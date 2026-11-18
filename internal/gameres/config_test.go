// SPDX-License-Identifier: GPL-3.0-or-later

package gameres

import (
	"reflect"
	"strings"
	"testing"
)

const configListHash = "0123456789abcdef0123456789abcdef"

func TestParseConfigRemoteProjection(t *testing.T) {
	data := `<config>
  <ResVersion> 2.4.1 </ResVersion><UpdateResVersion>0.0</UpdateResVersion><Section>2.4</Section>
  <UpdateBranchName>update_branch</UpdateBranchName><ResSize>700</ResSize><Tag>baseTag</Tag><Hash>decafe</Hash>
  <PreReleaseStatus>start</PreReleaseStatus><PreReleaseBranch>preview</PreReleaseBranch>
  <PreReleaseVersion>2.4.2</PreReleaseVersion><PreReleaseTestStatus>start</PreReleaseTestStatus>
  <PreReleaseTestBranch>preview_test</PreReleaseTestBranch><PreReleaseTestVersion>2.4.3</PreReleaseTestVersion>
  <BaseVerson><Res version="5.0.1"/><Res version="5.0.2" section="5.0"/><FutureTag/></BaseVerson>
  <FutureConfig><ResVersion>ignored</ResVersion></FutureConfig>
  <Extra><BaseTag><item name="baseTag"/></BaseTag><listHash>` + configListHash + `</listHash>
  <diffHash>abcdef0123456789abcdef0123456789</diffHash>
  <maxThreadCnt>5</maxThreadCnt><tagTaskThreadCnt>2</tagTaskThreadCnt><FutureExtra/></Extra>
</config>`
	got, err := ParseConfig([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		ResVersion: "2.4.1", UpdateResVersion: "0.0", Section: "2.4", UpdateBranch: "update_branch",
		ResSize: 700, CheckHash: "decafe", Tag: "baseTag", BaseTag: "baseTag",
		ListHash: configListHash, DiffHash: "abcdef0123456789abcdef0123456789",
		MaxThreadCnt: 5, TagTaskThreadCnt: 2, BaseVersions: []string{"5.0.1", "5.0.2"},
		PreReleaseStatus: "start", PreReleaseBranch: "preview", PreReleaseVersion: "2.4.2",
		PreReleaseTestStatus: "start", PreReleaseTestBranch: "preview_test", PreReleaseTestVersion: "2.4.3",
		UnknownFields: 3,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseConfig() = %#v; want %#v", got, want)
	}
}

func TestParseConfigLocalAndMinimal(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want Config
	}{
		{
			name: "local", body: `<LocalBranch>installed_branch</LocalBranch><BaseTag>baseTag</BaseTag><ResCount>3</ResCount>
<RemoteContent>opaque-local-cache</RemoteContent><Extra><ResSize>900</ResSize><listHash>` + configListHash + `</listHash></Extra>`,
			want: Config{ResVersion: "2.4.1", LocalBranch: "installed_branch", BaseTag: "baseTag", ResSize: 900, ListHash: configListHash},
		},
		{
			name: "minimal", body: `<Extra><listHash>` + configListHash + `</listHash></Extra>`,
			want: Config{ResVersion: "2.4.1", ListHash: configListHash},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseConfig([]byte(`<config><ResVersion>2.4.1</ResVersion>` + tc.body + `</config>`))
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ParseConfig() = %#v, %v; want %#v", got, err, tc.want)
			}
		})
	}
}

func TestParseConfigRejectsInvalidMetadata(t *testing.T) {
	valid := `<config><ResVersion>2.4.1</ResVersion><Extra><listHash>` + configListHash + `</listHash></Extra></config>`
	for name, data := range map[string]string{
		"wrong root":          strings.ReplaceAll(valid, "config", "ResList"),
		"missing version":     strings.ReplaceAll(valid, "ResVersion", "UnknownVersion"),
		"missing list hash":   strings.ReplaceAll(valid, "listHash", "UnknownHash"),
		"invalid list hash":   strings.ReplaceAll(valid, configListHash, strings.ToUpper(configListHash)),
		"unsafe version":      strings.ReplaceAll(valid, "2.4.1", "../release"),
		"duplicate version":   strings.Replace(valid, "</ResVersion>", "</ResVersion><ResVersion>2.4.2</ResVersion>", 1),
		"duplicate list hash": strings.Replace(valid, "</listHash>", "</listHash><listHash>"+configListHash+"</listHash>", 1),
		"namespaced version":  strings.Replace(valid, `<ResVersion>2.4.1</ResVersion>`, `<v:ResVersion xmlns:v="urn:other">2.4.1</v:ResVersion>`, 1),
		"negative size":       strings.Replace(valid, "<Extra>", "<ResSize>-1</ResSize><Extra>", 1),
		"overflowing size":    strings.Replace(valid, "<Extra>", "<ResSize>9223372036854775808</ResSize><Extra>", 1),
		"conflicting size":    strings.Replace(valid, "<Extra>", "<ResSize>1</ResSize><Extra><ResSize>2</ResSize>", 1),
		"ambiguous base tag":  strings.Replace(valid, "<Extra>", `<Extra><BaseTag><item name="baseTag"/><item name="other"/></BaseTag>`, 1),
		"repeated base tag":   strings.Replace(valid, "<Extra>", `<Extra><BaseTag>baseTag</BaseTag><BaseTag>baseTag</BaseTag>`, 1),
		"extra root":          valid + valid,
		"directive":           `<!DOCTYPE config SYSTEM "example.invalid">` + valid,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseConfig([]byte(data)); err == nil {
				t.Fatal("accepted invalid config")
			}
		})
	}
}
