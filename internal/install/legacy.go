package install

// legacyBundled holds the sha256 of every bundled skill file version
// spore shipped before ManifestName existed (0.47.0), so a first run
// over an old install replaces untouched copies. Frozen: newer
// versions are tracked by the manifest.
var legacyBundled = map[string][]string{
	"diagram/README.md": {
		"137fff39695f48ff6930fb9a104853c52fb31db2fd12b9c53710e3b89dbe5890",
	},
	"diagram/SKILL.md": {
		"20ab638bb2e0fafd3d046285d4f90967956eb9a3d235dde3aa396569689be53e",
		"58e4f01b3b89f40d8dd7c3bde61b4411f7916b73c43775df2d6a5112b055fa4f",
		"9e543df958d9d47a066b876a1e467229c64ac6e3a118a858a9554755692b8671",
	},
	"diagram/diagram": {
		"19612da82032c88941804e823b56e42000b1b215c015eb147d14e5e89974caae",
	},
	"select-agent/SKILL.md": {
		"41c3f8b82feeff5b2fa00b61f95219687338e4f98f99eb02f46fb2a3ebff209c",
		"47190f43f6845a0c24da98400192f2cac8b1c47e24f25e8f30950b728dd4502f",
		"4aa9b02b325fb12a481b476cafb7fbea76e92dc3a66255f274f56dd90257a13e",
		"c3a4b18ae294e44f7f69efbae8b890127e97578a2d70bef86bb686509847ee52",
	},
	"spore-bootstrap/README.md": {
		"167f0cdf449b4d45c4713ea9d806825f4180532739c6a12002a31a9e75d541c0",
	},
	"spore-bootstrap/SKILL.md": {
		"0e96d171c85eb5a3541642fd06e8e626fd67c63aab59e28afe4adbbe454b50a4",
		"3edc5c5b4e959b94a82d076ab58e0ead011f5463e81ce1b2522f068992a8cc66",
		"57d03ed5d606aa2848d2bd9eed8f12675bc95fe7dff9b867930edd6e9c23fc01",
		"89ce42ab94e6f372710771b2021da40332e9c9cc23c818c060209aca38f79ef4",
		"8f84ec11a2cee8ee250ac508e1d41ce4059edd15100472d19a67003652219fd5",
	},
}
