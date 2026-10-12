//go:build !unix

package cli

import "os"

// fileIdentity is unavailable here (os.FileInfo.Sys carries no file index on Windows):
// distinctOutputs falls back to os.SameFile among files sharing size and modification time.
func fileIdentity(os.FileInfo) (fileID, bool) { return fileID{}, false }
