// Package calibrate is the threshold calibration harness of the post-render verifier (VER-07,
// design §8): it builds good and corrupted candidates from a passage corpus, renders them with
// the said guard bypassed so every take reaches STT, splits them 60/40 stratified by
// construction, and evaluates the held-out part against labels made by listening. The same
// harness runs the full calibration per script, Latin and Han (VER-11).
package calibrate
