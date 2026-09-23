package release

// DownloadBase is where relay's published release assets live. It must not be
// overridable by an environment variable: nothing downloads from it, it is
// only printed.
const DownloadBase = "https://github.com/fuad-daoud/relay/releases/download"

// AssetURLs returns the release archive and checksums URL for tag on
// goos/goarch. Pure: it formats whatever it is given and has no error return,
// because callers pass only a tag ParseVersion accepted.
func AssetURLs(tag, goos, goarch string) (archive, checksums string) {
	base := DownloadBase + "/" + tag
	return base + "/relay_" + tag + "_" + goos + "_" + goarch + ".tar.gz",
		base + "/checksums.txt"
}
