/*
Copyright 2017 Google Inc. All Rights Reserved.
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at
    http://www.apache.org/licenses/LICENSE-2.0
Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package packages

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/GoogleCloudPlatform/compute-image-tools/osinfo"
	"github.com/StackExchange/wmi"
	ole "github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
	"github.com/google/logger"
)

var (
	googetDir = os.Getenv("GooGetRoot")
	googet    = filepath.Join(googetDir, "googet.exe")
)

// GetPackageUpdates gets available package updates GooGet as well as any
// available updates from Windows Update Agent.
func GetPackageUpdates() (map[string][]PkgInfo, []string) {
	pkgs := map[string][]PkgInfo{}
	var errs []string

	if exists(googet) {
		if googet, err := googetUpdates(); err != nil {
			msg := fmt.Sprintf("error getting googet updates: %v", err)
			logger.Error(msg)
			errs = append(errs, msg)
		} else {
			pkgs["googet"] = googet
		}
	}
	if wua, err := wuaUpdates("IsInstalled=0"); err != nil {
		msg := fmt.Sprintf("error listing installed Windows updates: %v", err)
		logger.Error(msg)
		errs = append(errs, msg)
	} else {
		pkgs["wua"] = wua
	}
	return pkgs, errs
}

func googetUpdates() ([]PkgInfo, error) {
	out, err := run(exec.Command(googet, "update"))
	if err != nil {
		return nil, err
	}

	/*
	   Searching for available updates...
	   foo.noarch, 3.5.4@1 --> 3.6.7@1 from repo
	   ...
	   Perform update? (y/N):
	*/
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")

	var pkgs []PkgInfo
	for _, ln := range lines[1:] {
		pkg := strings.Fields(ln)
		if len(pkg) != 6 {
			continue
		}

		p := strings.Split(pkg[0], ".")
		if len(p) != 2 {
			logger.Errorf("%s does not represent a package", ln)
			continue
		}
		pkgs = append(pkgs, PkgInfo{Name: p[0], Arch: strings.Trim(p[1], ","), Version: pkg[3]})
	}
	return pkgs, nil
}

type (
	IUpdateSession    = *ole.IDispatch
	IUpdateCollection = *ole.VARIANT
)

// wuaUpdates queries the Windows Update Agent API searcher with the provided query.
func wuaUpdates(query string) ([]PkgInfo, error) {
	connection := &ole.Connection{Object: nil}
	if err := connection.Initialize(); err != nil {
		return nil, err
	}
	defer connection.Uninitialize()

	unknown, err := oleutil.CreateObject("Microsoft.Update.Session")
	if err != nil {
		return nil, err
	}
	defer unknown.Release()

	session, err := unknown.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		return nil, err
	}
	defer session.Release()

	updtsRaw, err := GetWUAUpdates(session, query)
	if err != nil {
		return nil, err
	}
	defer updtsRaw.Clear()

	updts := updtsRaw.ToIDispatch()

	enumRaw, err := updts.GetProperty("_NewEnum")
	if err != nil {
		return nil, err
	}
	defer enumRaw.Clear()

	enum, err := enumRaw.ToIUnknown().IEnumVARIANT(ole.IID_IEnumVariant)
	if err != nil {
		return nil, err
	}
	if enum == nil {
		return nil, fmt.Errorf("can't get IEnumVARIANT, enum is nil")
	}
	defer enum.Release()

	var updates []PkgInfo
	for updtRaw, length, err := enum.Next(1); length > 0; updtRaw, length, err = enum.Next(1) {
		if err != nil {
			return nil, err
		}
		updt := updtRaw.ToIDispatch()
		defer updt.Release()

		titleRaw, err := updt.GetProperty("Title")
		if err != nil {
			return nil, err
		}
		name := titleRaw.ToString()
		ver := "unknown"
		// Match (KB4052623) or just KB4052623 for Defender patches.
		if start := strings.Index(name, "(KB"); start != -1 {
			if end := strings.Index(name[start:], ")"); end != -1 {
				ver = name[start+1 : start+end]
			}
		} else if start := strings.Index(name, "KB"); start != -1 {
			if end := strings.Index(name[start:], " "); end != -1 {
				ver = name[start : start+end]
			}
		}
		updates = append(updates, PkgInfo{Name: name, Arch: osinfo.Architecture(runtime.GOARCH), Version: ver})
	}

	return updates, nil
}

// DownloadWUAUpdates downloads all updates in a IUpdateCollection
func DownloadWUAUpdates(session IUpdateSession, updates IUpdateCollection) error {
	// returns IUpdateDownloader
	// https://docs.microsoft.com/en-us/windows/desktop/api/wuapi/nn-wuapi-iupdatedownloader
	downloaderRaw, err := session.CallMethod("CreateUpdateDownloader")
	if err != nil {
		return fmt.Errorf("error calling method CreateUpdateDownloader on IUpdateSession: %v", err)
	}
	downloader := downloaderRaw.ToIDispatch()
	defer downloaderRaw.Clear()

	if _, err := downloader.PutProperty("Updates", updates); err != nil {
		return fmt.Errorf("error calling PutProperty Updates on IUpdateDownloader: %v", err)
	}

	logger.Infof("Downloading updates")
	if _, err := downloader.CallMethod("Download"); err != nil {
		return fmt.Errorf("error calling method Download on IUpdateDownloader: %v", err)
	}
	return nil
}

// InstallWUAUpdates installs all updates in a IUpdateCollection
func InstallWUAUpdates(session IUpdateSession, updates IUpdateCollection) error {
	// returns IUpdateInstaller
	// https://docs.microsoft.com/en-us/windows/desktop/api/wuapi/nf-wuapi-iupdatesession-createupdateinstaller
	installerRaw, err := session.CallMethod("CreateUpdateInstaller")
	if err != nil {
		return fmt.Errorf("error calling method CreateUpdateInstaller on IUpdateSession: %v", err)
	}
	installer := installerRaw.ToIDispatch()
	defer installerRaw.Clear()

	if _, err := installer.PutProperty("Updates", updates); err != nil {
		return fmt.Errorf("error calling PutProperty Updates on IUpdateInstaller: %v", err)
	}

	logger.Infof("Installing updates")
	if _, err := installer.CallMethod("Install", updates); err != nil {
		return fmt.Errorf("error calling method Install on IUpdateInstaller: %v", err)
	}
	return nil
}

// GetWUAUpdates queries the Windows Update Agent API searcher with the provided query
// and returns a IUpdateCollection.
func GetWUAUpdates(session IUpdateSession, query string) (IUpdateCollection, error) {
	// returns IUpdateSearcher
	// https://msdn.microsoft.com/en-us/library/windows/desktop/aa386515(v=vs.85).aspx
	searcherRaw, err := session.CallMethod("CreateUpdateSearcher")
	if err != nil {
		return nil, err
	}
	searcher := searcherRaw.ToIDispatch()
	defer searcherRaw.Clear()

	logger.Infof("Querying Windows Update Agent Search with query %q.", query)
	// returns ISearchResult
	// https://msdn.microsoft.com/en-us/library/windows/desktop/aa386077(v=vs.85).aspx
	resultRaw, err := searcher.CallMethod("Search", query)
	if err != nil {
		return nil, fmt.Errorf("error calling method Search on IUpdateSearcher: %v", err)
	}
	result := resultRaw.ToIDispatch()
	defer resultRaw.Clear()

	// returns IUpdateCollection
	// https://msdn.microsoft.com/en-us/library/windows/desktop/aa386107(v=vs.85).aspx
	return result.GetProperty("Updates")
}

// GetInstalledPackages gets all installed GooGet packages and Windows updates.
// Windows updates are read from Windows Update Agent and Win32_QuickFixEngineering.
func GetInstalledPackages() (map[string][]PkgInfo, []string) {
	pkgs := map[string][]PkgInfo{}
	var errs []string

	if exists(googet) {
		if googet, err := installedGooGetPackages(); err != nil {
			msg := fmt.Sprintf("error listing installed googet packages: %v", err)
			logger.Error(msg)
			errs = append(errs, msg)
		} else {
			pkgs["googet"] = googet
		}
	}

	if wua, err := wuaUpdates("IsInstalled=1"); err != nil {
		msg := fmt.Sprintf("error listing installed Windows updates: %v", err)
		logger.Error(msg)
		errs = append(errs, msg)
	} else {
		pkgs["wua"] = wua
	}

	if qfe, err := quickFixEngineering(); err != nil {
		msg := fmt.Sprintf("error listing installed QuickFixEngineering updates: %v", err)
		logger.Error(msg)
		errs = append(errs, msg)
	} else {
		pkgs["qfe"] = qfe
	}

	return pkgs, errs
}

func installedGooGetPackages() ([]PkgInfo, error) {
	out, err := run(exec.Command(googet, "installed"))
	if err != nil {
		return nil, err
	}

	/*
			   Installed Packages:
			   foo.x86_64 1.2.3@4
			   bar.noarch 1.2.3@4
		     ...
	*/
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")

	if len(lines) <= 1 {
		logger.Info("No packages installed.")
		return nil, nil
	}

	var pkgs []PkgInfo
	for _, ln := range lines[1:] {
		pkg := strings.Fields(ln)
		if len(pkg) != 2 {
			logger.Errorf("%s does not represent a package", ln)
			continue
		}

		p := strings.Split(pkg[0], ".")
		if len(p) != 2 {
			logger.Errorf("%s does not represent a package", ln)
			continue
		}

		pkgs = append(pkgs, PkgInfo{Name: p[0], Arch: p[1], Version: pkg[1]})
	}
	return pkgs, nil
}

type win32_QuickFixEngineering struct {
	HotFixID string
}

// quickFixEngineering queries the wmi object win32_QuickFixEngineering for a list of installed updates.
func quickFixEngineering() ([]PkgInfo, error) {
	var updts []win32_QuickFixEngineering
	logger.Info("Querying WMI for installed QuickFixEngineering updates.")
	if err := wmi.Query(wmi.CreateQuery(&updts, ""), &updts); err != nil {
		return nil, err
	}
	var qfe []PkgInfo
	for _, update := range updts {
		qfe = append(qfe, PkgInfo{Name: update.HotFixID, Arch: osinfo.Architecture(runtime.GOARCH), Version: update.HotFixID})
	}
	return qfe, nil
}
