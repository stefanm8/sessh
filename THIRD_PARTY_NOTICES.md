# Third-party notices

sessh is MIT-licensed. The following modules are linked into the supported
macOS/Linux binaries and retain their upstream terms. License and notice
texts are copied unchanged from the versions listed in `go.mod` and `go.sum`.

| Module | Version | License / notice text |
| --- | --- | --- |
| `github.com/deckarep/golang-set/v2` | `v2.8.0` | [LICENSE](third_party/licenses/github.com_deckarep_golang-set_v2/LICENSE) |
| `github.com/go-jose/go-jose/v3` | `v3.0.5` | [LICENSE](third_party/licenses/github.com_go-jose_go-jose_v3/LICENSE), [json/LICENSE](third_party/licenses/github.com_go-jose_go-jose_v3/json/LICENSE) |
| `github.com/go-stack/stack` | `v1.8.1` | [LICENSE.md](third_party/licenses/github.com_go-stack_stack/LICENSE.md) |
| `github.com/mxschmitt/playwright-go` | `v0.6100.0` | [LICENSE](third_party/licenses/github.com_mxschmitt_playwright-go/LICENSE) |
| `github.com/spf13/cobra` | `v1.10.2` | [LICENSE.txt](third_party/licenses/github.com_spf13_cobra/LICENSE.txt) |
| `github.com/spf13/pflag` | `v1.0.9` | [LICENSE](third_party/licenses/github.com_spf13_pflag/LICENSE) |
| `gopkg.in/yaml.v3` | `v3.0.1` | [LICENSE](third_party/licenses/gopkg.in_yaml.v3/LICENSE), [NOTICE](third_party/licenses/gopkg.in_yaml.v3/NOTICE) |

Compiled Go binaries also include the Go runtime and standard library under
[the Go license](third_party/licenses/go/LICENSE), with additional vendored
package notices under `third_party/licenses/go/vendor`. Update these copies
when upgrading dependencies or the toolchain. Test-only dependencies are not
bundled into release binaries; their licenses remain in the Go module cache.

Playwright's Node driver and Chromium are installed separately and are not
included in release archives. Their distributions contain their own license
and notice files.
