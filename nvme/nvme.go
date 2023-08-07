// Copyright 2017-2022 Daniel Swarbrick. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package nvme

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math/big"
	"unsafe"

	"github.com/dswarbrick/go-nvme/ioctl"

	"golang.org/x/sys/unix"
)

var (
	// Defined in <linux/nvme_ioctl.h>
	NVME_IOCTL_ADMIN_CMD = ioctl.Iowr('N', 0x41, unsafe.Sizeof(nvmePassthruCommand{}))
)

type NVMeDevice struct {
	Name string
	fd   int
}

func NewNVMeDevice(name string) *NVMeDevice {
	return &NVMeDevice{name, -1}
}

func (d *NVMeDevice) Open() (err error) {
	d.fd, err = unix.Open(d.Name, unix.O_RDWR, 0600)
	return err
}

func (d *NVMeDevice) Close() error {
	return unix.Close(d.fd)
}

func (d *NVMeDevice) IdentifyController(w io.Writer) (NVMeController, error) {
	var buf [4096]byte

	cmd := nvmePassthruCommand{
		opcode:   NVME_ADMIN_IDENTIFY,
		nsid:     0, // Namespace 0, since we are identifying the controller
		addr:     uint64(uintptr(unsafe.Pointer(&buf[0]))),
		data_len: uint32(len(buf)),
		cdw10:    1, // Identify controller
	}

	if err := ioctl.Ioctl(uintptr(d.fd), NVME_IOCTL_ADMIN_CMD, uintptr(unsafe.Pointer(&cmd))); err != nil {
		return NVMeController{}, err
	}

	fmt.Fprintf(w, "NVMe call: opcode=%#02x, size=%#04x, nsid=%#08x, cdw10=%#08x\n",
		cmd.opcode, cmd.data_len, cmd.nsid, cmd.cdw10)

	var idCtrlr nvmeIdentController

	binary.Read(bytes.NewBuffer(buf[:]), NativeEndian, &idCtrlr)

	controller := NVMeController{
		VendorID:        idCtrlr.VendorID,
		ModelNumber:     string(idCtrlr.ModelNumber[:]),
		SerialNumber:    string(bytes.TrimSpace(idCtrlr.SerialNumber[:])),
		FirmwareVersion: string(idCtrlr.Firmware[:]),
		MaxDataXferSize: 1 << idCtrlr.Mdts,
		// Convert IEEE OUI ID from big-endian
		OUI: uint32(idCtrlr.IEEE[0]) | uint32(idCtrlr.IEEE[1])<<8 | uint32(idCtrlr.IEEE[2])<<16,
	}

	fmt.Fprintln(w)
	controller.Print(w)

	for _, ps := range idCtrlr.Psd {
		if ps.MaxPower > 0 {
			fmt.Fprintf(w, "%+v\n", ps)
		}
	}

	return controller, nil
}

func (d *NVMeDevice) IdentifyNamespace(w io.Writer, namespace uint32) error {
	var buf [4096]byte

	cmd := nvmePassthruCommand{
		opcode:   NVME_ADMIN_IDENTIFY,
		nsid:     namespace,
		addr:     uint64(uintptr(unsafe.Pointer(&buf[0]))),
		data_len: uint32(len(buf)),
		cdw10:    0,
	}

	if err := ioctl.Ioctl(uintptr(d.fd), NVME_IOCTL_ADMIN_CMD, uintptr(unsafe.Pointer(&cmd))); err != nil {
		return err
	}

	fmt.Fprintf(w, "NVMe call: opcode=%#02x, size=%#04x, nsid=%#08x, cdw10=%#08x\n",
		cmd.opcode, cmd.data_len, cmd.nsid, cmd.cdw10)

	var ns nvmeIdentNamespace

	binary.Read(bytes.NewBuffer(buf[:]), NativeEndian, &ns)

	fmt.Fprintf(w, "Namespace %d size: %d sectors\n", namespace, ns.Nsze)
	fmt.Fprintf(w, "Namespace %d utilisation: %d sectors\n", namespace, ns.Nuse)

	return nil
}

func (d *NVMeDevice) getSMART() (NvmeSMART, error) {
	buf := make([]byte, 512)

	// Read SMART log
	if err := d.readLogPage(0x02, &buf); err != nil {
		return NvmeSMART{}, err
	}
	var sl nvmeSMARTLog
	err := binary.Read(bytes.NewBuffer(buf[:]), NativeEndian, &sl)
	if err != nil {
		return NvmeSMART{}, err
	}
	ret := NvmeSMART{
		CritWarning:      sl.CritWarning,
		Temperature:      ((uint16(sl.Temperature[0]) | uint16(sl.Temperature[1])<<8) - 273), // Kelvin to degrees Celsius
		AvailSpare:       sl.AvailSpare,
		SpareThresh:      sl.SpareThresh,
		PercentUsed:      sl.PercentUsed,
		DataUnitsRead:    le128ToBigInt(sl.DataUnitsRead),
		DataUnitsWritten: le128ToBigInt(sl.DataUnitsWritten),
		HostReads:        le128ToBigInt(sl.HostReads),
		HostWrites:       le128ToBigInt(sl.HostWrites),
		CtrlBusyTime:     le128ToBigInt(sl.CtrlBusyTime),
		PowerCycles:      le128ToBigInt(sl.PowerCycles),
		PowerOnHours:     le128ToBigInt(sl.PowerOnHours),
		UnsafeShutdowns:  le128ToBigInt(sl.UnsafeShutdowns),
		MediaErrors:      le128ToBigInt(sl.MediaErrors),
		NumErrLogEntries: le128ToBigInt(sl.NumErrLogEntries),
		TempSensor1:      tempC(sl.TempSensors[0:2]),
		TempSensor2:      tempC(sl.TempSensors[2:4]),
		TempSensor3:      tempC(sl.TempSensors[4:6]),
		TempSensor4:      tempC(sl.TempSensors[6:8]),
		TempSensor5:      tempC(sl.TempSensors[8:10]),
		TempSensor6:      tempC(sl.TempSensors[10:12]),
		TempSensor7:      tempC(sl.TempSensors[12:14]),
		TempSensor8:      tempC(sl.TempSensors[14:16]),
		TM1TransCount:    sl.TM1TransCount,
		TM2TransCount:    sl.TM2TransCount,
		TM1TotalTime:     sl.TM1TotalTime,
		TM2TotalTime:     sl.TM2TotalTime,
	}
	return ret, nil
}

func (d *NVMeDevice) GetSMART() (NvmeSMART, error) {
	return d.getSMART()

}

func (d *NVMeDevice) PrintSMART(w io.Writer) error {
	sl, err := d.getSMART()
	if err != nil {
		return err
	}
	unit := big.NewInt(512 * 1000)
	fmt.Fprintln(w, "\nSMART data follows Foo:")
	fmt.Fprintf(w, "Critical warning: %#02x\n", sl.CritWarning)
	fmt.Fprintf(w, "Temperature: %d° Celsius\n", sl.Temperature)
	fmt.Fprintf(w, "Avail. spare: %d%%\n", sl.AvailSpare)
	fmt.Fprintf(w, "Avail. spare threshold: %d%%\n", sl.SpareThresh)
	fmt.Fprintf(w, "Percentage used: %d%%\n", sl.PercentUsed)
	fmt.Fprintf(w, "Data units read: %d [%s]\n",
		sl.DataUnitsRead, formatBigBytes(new(big.Int).Mul(sl.DataUnitsRead, unit)))
	fmt.Fprintf(w, "Data units written: %d [%s]\n",
		sl.DataUnitsWritten, formatBigBytes(new(big.Int).Mul(sl.DataUnitsWritten, unit)))
	fmt.Fprintf(w, "Host read commands: %d\n", sl.HostReads)
	fmt.Fprintf(w, "Host write commands: %d\n", sl.HostWrites)
	fmt.Fprintf(w, "Controller busy time: %d\n", sl.CtrlBusyTime)
	fmt.Fprintf(w, "Power cycles: %d\n", sl.PowerCycles)
	fmt.Fprintf(w, "Power on hours: %d\n", sl.PowerOnHours)
	fmt.Fprintf(w, "Unsafe shutdowns: %d\n", sl.UnsafeShutdowns)
	fmt.Fprintf(w, "Media & data integrity errors: %d\n", sl.MediaErrors)
	fmt.Fprintf(w, "Error information log entries: %d\n", sl.NumErrLogEntries)
	fmt.Fprintf(w, "Warning composite temperature time: %d\n", sl.WarningTempTime)
	fmt.Fprintf(w, "Critical composite temperature time: %d\n", sl.CritCompTime)
	printTempSensor(1, w, sl.TempSensor1)
	printTempSensor(2, w, sl.TempSensor2)
	printTempSensor(3, w, sl.TempSensor3)
	printTempSensor(4, w, sl.TempSensor4)
	printTempSensor(5, w, sl.TempSensor5)
	printTempSensor(6, w, sl.TempSensor6)
	printTempSensor(7, w, sl.TempSensor7)
	printTempSensor(8, w, sl.TempSensor8)
	fmt.Fprintf(w, "Thermal Management T1 Trans Count: %d\n", sl.TM1TransCount)
	fmt.Fprintf(w, "Thermal Management TT Trans Count: %d\n", sl.TM2TransCount)
	fmt.Fprintf(w, "Thermal Management T1 Total Time: %d\n", sl.TM1TotalTime)
	fmt.Fprintf(w, "Thermal Management T2 Total Time: %d\n", sl.TM2TotalTime)

	return nil
}

func printTempSensor(id int, w io.Writer, sensor *uint16) {
	if sensor != nil {
		fmt.Fprintf(w, "Temperature sensor %d: %d° Celsius\n", id, *sensor)
	}

}

// parse temperature in Kelvin to degrees Celsius, or nil if not provided
// https://nvmexpress.org/wp-content/uploads/NVM-Express-Base-Specification-2.0c-2022.10.04-Ratified.pdf
func tempC(b []uint16) *uint16 {
	if b[0]|b[1] == 0 {
		return nil
	}
	t := ((uint16(b[0]) | uint16(b[1])<<8) - 273)
	return &t
}

func (d *NVMeDevice) readLogPage(logID uint8, buf *[]byte) error {
	bufLen := len(*buf)

	if (bufLen < 4) || (bufLen > 0x4000) || (bufLen%4 != 0) {
		return fmt.Errorf("invalid buffer size")
	}

	cmd := nvmePassthruCommand{
		opcode:   NVME_ADMIN_GET_LOG_PAGE,
		nsid:     0xffffffff, // FIXME
		addr:     uint64(uintptr(unsafe.Pointer(&(*buf)[0]))),
		data_len: uint32(bufLen),
		cdw10:    uint32(logID) | (((uint32(bufLen) / 4) - 1) << 16),
	}

	return ioctl.Ioctl(uintptr(d.fd), NVME_IOCTL_ADMIN_CMD, uintptr(unsafe.Pointer(&cmd)))
}

type nvmeIdentPowerState struct {
	MaxPower        uint16 // Centiwatts
	Rsvd2           uint8
	Flags           uint8
	EntryLat        uint32 // Microseconds
	ExitLat         uint32 // Microseconds
	ReadTput        uint8
	ReadLat         uint8
	WriteTput       uint8
	WriteLat        uint8
	IdlePower       uint16
	IdleScale       uint8
	Rsvd19          uint8
	ActivePower     uint16
	ActiveWorkScale uint8
	Rsvd23          [9]byte
}

type nvmeLBAF struct {
	Ms uint16
	Ds uint8
	Rp uint8
}

type nvmeIdentNamespace struct {
	Nsze    uint64
	Ncap    uint64
	Nuse    uint64
	Nsfeat  uint8
	Nlbaf   uint8
	Flbas   uint8
	Mc      uint8
	Dpc     uint8
	Dps     uint8
	Nmic    uint8
	Rescap  uint8
	Fpi     uint8
	Rsvd33  uint8
	Nawun   uint16
	Nawupf  uint16
	Nacwu   uint16
	Nabsn   uint16
	Nabo    uint16
	Nabspf  uint16
	Rsvd46  [2]byte
	Nvmcap  [16]byte
	Rsvd64  [40]byte
	Nguid   [16]byte
	EUI64   [8]byte
	Lbaf    [16]nvmeLBAF
	Rsvd192 [192]byte
	Vs      [3712]byte
} // 4096 bytes

type nvmeSMARTLog struct {
	CritWarning      uint8
	Temperature      [2]uint8
	AvailSpare       uint8
	SpareThresh      uint8
	PercentUsed      uint8
	Rsvd6            [26]byte
	DataUnitsRead    [16]byte
	DataUnitsWritten [16]byte
	HostReads        [16]byte
	HostWrites       [16]byte
	CtrlBusyTime     [16]byte
	PowerCycles      [16]byte
	PowerOnHours     [16]byte
	UnsafeShutdowns  [16]byte
	MediaErrors      [16]byte
	NumErrLogEntries [16]byte
	WarningTempTime  uint32
	CritCompTime     uint32
	TempSensors      [16]uint16
	TM1TransCount    uint32
	TM2TransCount    uint32
	TM1TotalTime     uint32
	TM2TotalTime     uint32
	Rsvd216          [264]byte
} // 512 bytes

// NVMe SMART/Health Information
type NvmeSMART struct {
	CritWarning      uint8
	Temperature      uint16
	AvailSpare       uint8
	SpareThresh      uint8
	PercentUsed      uint8
	Rsvd6            [26]byte
	DataUnitsRead    *big.Int
	DataUnitsWritten *big.Int
	HostReads        *big.Int
	HostWrites       *big.Int
	CtrlBusyTime     *big.Int
	PowerCycles      *big.Int
	PowerOnHours     *big.Int
	UnsafeShutdowns  *big.Int
	MediaErrors      *big.Int
	NumErrLogEntries *big.Int
	WarningTempTime  uint32
	CritCompTime     uint32
	TempSensor1      *uint16
	TempSensor2      *uint16
	TempSensor3      *uint16
	TempSensor4      *uint16
	TempSensor5      *uint16
	TempSensor6      *uint16
	TempSensor7      *uint16
	TempSensor8      *uint16
	TM1TransCount    uint32
	TM2TransCount    uint32
	TM1TotalTime     uint32
	TM2TotalTime     uint32
}
