package release

// DownloadBase is where relevo's published release assets live. It stays a
// constant so that no environment variable can redirect a binary download:
// `relevo update` downloads the archive and checksums from here, and the
// integrity guard is the checksum verified against this base's checksums.txt.
const DownloadBase = "https://github.com/fuad-daoud/relevo/releases/download"

// AssetURLs returns the release archive and checksums URL for tag on
// goos/goarch. Pure: it formats whatever it is given and has no error return,
// because callers pass only a tag IsReleaseTag accepts; FetchBinary checks it
// again before any request.
func AssetURLs(tag, goos, goarch string) (archive, checksums string) {
	return assetURLsFrom(DownloadBase, tag, goos, goarch)
}

// assetURLsFrom is AssetURLs against an arbitrary base. Tests pass an httptest
// URL; the CLI always passes DownloadBase.
func assetURLsFrom(base, tag, goos, goarch string) (archive, checksums string) {
	root := base + "/" + tag
	return root + "/relevo_" + tag + "_" + goos + "_" + goarch + ".tar.gz",
		root + "/checksums.txt"
}
