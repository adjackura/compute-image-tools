//  Copyright 2018 Google Inc. All Rights Reserved.
//
//  Licensed under the Apache License, Version 2.0 (the "License");
//  you may not use this file except in compliance with the License.
//  You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
//  Unless required by applicable law or agreed to in writing, software
//  distributed under the License is distributed on an "AS IS" BASIS,
//  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//  See the License for the specific language governing permissions and
//  limitations under the License.

// instance_inventory pretty prints out an instances inventory data.
package main

import (
	"fmt"

	"github.com/GoogleCloudPlatform/compute-image-tools/package_library"
	ole "github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
	"github.com/google/logger"
)

func installWUAUpdates(query string) error {
	connection := &ole.Connection{Object: nil}
	if err := connection.Initialize(); err != nil {
		return err
	}
	defer connection.Uninitialize()

	unknown, err := oleutil.CreateObject("Microsoft.Update.Session")
	if err != nil {
		return err
	}
	defer unknown.Release()

	session, err := unknown.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		return err
	}
	defer session.Release()

	updtsRaw, err := packages.GetWUAUpdates(session, query)
	if err != nil {
		return fmt.Errorf("GetWUAUpdates error: %v", err)
	}
	defer updtsRaw.Clear()

	updts := updtsRaw.ToIDispatch()
	count, err := updts.GetProperty("Count")
	if err != nil {
		return fmt.Errorf("GetProperty Count: %v", err)
	}
	defer count.Clear()

	updtCnt, _ := count.Value().(int32)

	if updtCnt == 0 {
		logger.Infof("No updates to install")
		return nil
	}

	logger.Infof("%d updates available", updtCnt)

	var msg string
	for i := 0; i < int(updtCnt); i++ {
		if err := func() error {
			updtRaw, err := updts.GetProperty("Item", i)
			if err != nil {
				return err
			}
			defer updtRaw.Clear()

			updt := updtRaw.ToIDispatch()
			defer updt.Release()

			title, err := updt.GetProperty("Title")
			if err != nil {
				return err
			}
			defer title.Clear()

			eula, err := updt.GetProperty("EulaAccepted")
			if err != nil {
				updtRaw.Clear()
				return err
			}
			defer eula.Clear()

			msg += fmt.Sprintf("  %s\n  - EulaAccepted: %v\n", title.Value(), eula.Value())
			return nil
		}(); err != nil {
			return err
		}
	}

	logger.Infof("Packages to be installed:\n%s", msg)

	if err := packages.DownloadWUAUpdates(session, updtsRaw); err != nil {
		return fmt.Errorf("DownloadWUAUpdates error: %v", err)
	}

	return nil
	if err := packages.InstallWUAUpdates(session, updtsRaw); err != nil {
		return fmt.Errorf("InstallWUAUpdates error: %v", err)
	}
	return nil
}

func main() {
	if err := installWUAUpdates("isinstalled=0"); err != nil {
		fmt.Println("ERROR:", err)
	}
}
