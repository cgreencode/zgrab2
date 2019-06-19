// Package redis provides a zgrab2 Module that probes for redis services.
// The default port for redis is TCP 6379, and it is a cleartext protocol
// defined at https://redis.io/topics/protocol.
// Servers can be configured to require (cleartext) password authentication,
// which is omitted from our probe by default (pass --password <your password>
// to supply one).
// Further, admins can rename commands, so even if authentication is not
// required we may not get the expected output.
// However, we should always get output in the expected format, which is fairly
// distinct. The probe sends a sequence of commands and checks that the response
// is well-formed redis data, which should be possible whatever the
// configuration.
package redis

import (
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"path/filepath"
	"strings"

	log "github.com/sirupsen/logrus"
	"github.com/zmap/zgrab2"
	"gopkg.in/yaml.v2"
)

// Flags contains redis-specific command-line flags.
type Flags struct {
	zgrab2.BaseFlags

	CustomCommands string `long:"custom-commands" description:"Pathname for JSON/YAML file that contains extra commands to execute. WARNING: This is sent in the clear."`
	Mappings       string `long:"mappings" description:"Pathname for JSON/YAML file that contains mappings for command names."`
	Password       string `long:"password" description:"Set a password to use to authenticate to the server. WARNING: This is sent in the clear."`
	DoInline       bool   `long:"inline" description:"Send commands using the inline syntax"`
	Verbose        bool   `long:"verbose" description:"More verbose logging, include debug fields in the scan results"`
}

// Module implements the zgrab2.Module interface
type Module struct {
}

// Scanner implements the zgrab2.Scanner interface
type Scanner struct {
	config          *Flags
	commandMappings map[string]interface{}
	customCommands  []string
}

// scan holds the state for the scan of an individual target
type scan struct {
	scanner *Scanner
	result  *Result
	target  *zgrab2.ScanTarget
	conn    *Connection
	close   func()
}

// Result is the struct that is returned by the scan.
// If authentication is required, most responses can have the value
// "(error: NOAUTH Authentication required.)"
type Result struct {
	// Commands is the list of commands actually sent to the server, serialized
	// in inline format (e.g. COMMAND arg1 "arg 2" arg3)
	Commands []string `json:"commands,omitempty" zgrab:"debug"`

	// RawCommandOutput is the output returned by the server for each command sent;
	// the index in RawCommandOutput matches the index in Commands.
	RawCommandOutput [][]byte `json:"raw_command_output,omitempty" zgrab:"debug"`

	// PingResponse is the response from the server, should be the simple string
	// "PONG".
	// NOTE: This is invoked *before* calling AUTH, so this may return an auth
	// required error even if --password is provided.
	PingResponse string `json:"ping_response,omitempty"`

	// AuthResponse is only included if --password is set.
	AuthResponse string `json:"auth_response,omitempty"`

	// InfoResponse is the response from the INFO command: "Lines can contain a
	// section name (starting with a # character) or a property. All the
	// properties are in the form of field:value terminated by \r\n."
	InfoResponse string `json:"info_response,omitempty"`

	// Version is read from the InfoResponse (the field "server_version"), if
	// present.
	Version string `json:"version,omitempty"`

	// OS is read from the InfoResponse (the field "os"), if present. It specifies
	// the OS the redis server is running.
	OS string `json:"os,omitempty"`

	// NonexistentResponse is the response to the non-existent command; even if
	// auth is required, this may give a different error than existing commands.
	NonexistentResponse string `json:"nonexistent_response,omitempty"`

	// CustomResponses is an array that holds the commands, arguments, and
	// responses from user-inputted commands.
	CustomResponses []CustomResponse `json:"custom_responses,omitempty"`

	// QuitResponse is the response from the QUIT command -- should be the
	// simple string "OK" even when authentication is required, unless the
	// QUIT command was renamed.
	QuitResponse string `json:"quit_response,omitempty"`
}

// RegisterModule registers the zgrab2 module
func RegisterModule() {
	var module Module
	_, err := zgrab2.AddCommand("redis", "redis", "Probe for redis", 6379, &module)
	if err != nil {
		log.Fatal(err)
	}
}

// NewFlags provides an empty instance of the flags that will be filled in by the framework
func (module *Module) NewFlags() interface{} {
	return new(Flags)
}

// NewScanner provides a new scanner instance
func (module *Module) NewScanner() zgrab2.Scanner {
	return new(Scanner)
}

// Validate checks that the flags are valid
func (flags *Flags) Validate(args []string) error {
	return nil
}

// Help returns the module's help string
func (flags *Flags) Help() string {
	return ""
}

// Init initializes the scanner
func (scanner *Scanner) Init(flags zgrab2.ScanFlags) error {
	f, _ := flags.(*Flags)
	scanner.config = f
	err := scanner.initCommands()
	if err != nil {
		log.Fatal(err)
	}
	return nil
}

// InitPerSender initializes the scanner for a given sender
func (scanner *Scanner) InitPerSender(senderID int) error {
	return nil
}

// GetName returns the name of the scanner
func (scanner *Scanner) GetName() string {
	return scanner.config.Name
}

// GetTrigger returns the Trigger defined in the Flags.
func (scanner *Scanner) GetTrigger() string {
	return scanner.config.Trigger
}

// GetPort returns the port being scanned
func (scanner *Scanner) GetPort() uint {
	return scanner.config.Port
}

// Close cleans up the scanner.
func (scan *scan) Close() {
	defer scan.close()
}

func getUnmarshaler(file string) (func([]byte, interface{}) error, error) {
	var unmarshaler func([]byte, interface{}) error
	switch ext := filepath.Ext(file); ext {
	case ".json":
		unmarshaler = json.Unmarshal
	case ".yaml", ".yml":
		unmarshaler = yaml.Unmarshal
	default:
		err := fmt.Errorf("File type %s not valid.", ext)
		return nil, err
	}
	return unmarshaler, nil
}

func getFileContents(file string, output interface{}) error {
	unmarshaler, err := getUnmarshaler(file)
	if err != nil {
		return err
	}
	fileContent, err := ioutil.ReadFile(file)
	if err != nil {
		return err
	}
	err = unmarshaler([]byte(fileContent), output)
	if err != nil {
		return err
	}

	return nil
}

// Initializes the command mappings
func (scanner *Scanner) initCommands() error {
	scanner.commandMappings = map[string]interface{}{
		"PING":        "PING",
		"AUTH":        "AUTH",
		"INFO":        "INFO",
		"NONEXISTENT": "NONEXISTENT",
		"QUIT":        "QUIT",
	}

	if scanner.config.CustomCommands != "" {
		var customCommands []string
		err := getFileContents(scanner.config.CustomCommands, &customCommands)
		if err != nil {
			return err
		}
		scanner.customCommands = customCommands
	}

	// User supplied a file for updated command mappings
	if scanner.config.Mappings != "" {
		var mappings map[string]string
		err := getFileContents(scanner.config.Mappings, &mappings)
		if err != nil {
			return err
		}
		for origCommand, newCommand := range mappings {
			scanner.commandMappings[strings.ToUpper(origCommand)] = strings.ToUpper(newCommand)
		}
	}

	return nil
}

// SendCommand sends the given command/args to the server, using the scanner's
// configuration, and drop the command/output into the result.
func (scan *scan) SendCommand(cmd string, args ...string) (RedisValue, error) {
	exec := scan.conn.SendCommand
	scan.result.Commands = append(scan.result.Commands, getInlineCommand(cmd, args...))
	if scan.scanner.config.DoInline {
		exec = scan.conn.SendInlineCommand
	}
	ret, err := exec(cmd, args...)
	if err != nil {
		return nil, err
	}
	scan.result.RawCommandOutput = append(scan.result.RawCommandOutput, ret.Encode())
	return ret, nil
}

// StartScan opens a connection to the target and sets up a scan instance for it
func (scanner *Scanner) StartScan(target *zgrab2.ScanTarget) (*scan, error) {
	conn, err := target.Open(&scanner.config.BaseFlags)
	if err != nil {
		return nil, err
	}
	return &scan{
		target:  target,
		scanner: scanner,
		result:  &Result{},
		conn: &Connection{
			scanner: scanner,
			conn:    conn,
		},
		close: func() { conn.Close() },
	}, nil
}

// Force the response into a string. Used when you expect a human-readable
// string.
func forceToString(val RedisValue) string {
	switch v := val.(type) {
	case SimpleString:
		return string(v)
	case BulkString:
		return string([]byte(v))
	case Integer:
		return fmt.Sprintf("%d", v)
	case ErrorMessage:
		return fmt.Sprintf("(Error: %s)", string(v))
	case NullType:
		return "<null>"
	case RedisArray:
		return "(Unexpected array)"
	default:
		panic("unreachable")
	}
}

// Protocol returns the protocol identifer for the scanner.
func (s *Scanner) Protocol() string {
	return "redis"
}

// Scan executes the following commands:
// 1. PING
// 2. (only if --password is provided) AUTH <password>
// 3. INFO
// 4. NONEXISTENT
// 5. QUIT
// The responses for each of these is logged, and if INFO succeeds, the version
// is scraped from it.
func (scanner *Scanner) Scan(target zgrab2.ScanTarget) (zgrab2.ScanStatus, interface{}, error) {
	// ping, info, quit
	scan, err := scanner.StartScan(&target)
	if err != nil {
		return zgrab2.TryGetScanStatus(err), nil, err
	}
	defer scan.Close()
	result := scan.result
	pingResponse, err := scan.SendCommand(scanner.commandMappings["PING"].(string))
	if err != nil {
		// If the first command fails (as opposed to succeeding but returning an
		// ErrorMessage response), then flag the probe as having failed.
		return zgrab2.TryGetScanStatus(err), nil, err
	}
	// From this point forward, we always return a non-nil result, implying that
	// we have positively identified that a redis service is present.
	result.PingResponse = forceToString(pingResponse)
	if scanner.config.Password != "" {
		authResponse, err := scan.SendCommand(scanner.commandMappings["AUTH"].(string), scanner.config.Password)
		if err != nil {
			return zgrab2.TryGetScanStatus(err), result, err
		}
		result.AuthResponse = forceToString(authResponse)
	}
	infoResponse, err := scan.SendCommand(scanner.commandMappings["INFO"].(string))
	if err != nil {
		return zgrab2.TryGetScanStatus(err), result, err
	}
	result.InfoResponse = forceToString(infoResponse)
	if infoResponseBulk, ok := infoResponse.(BulkString); ok {
		version_found, os_found := false, false
		for _, line := range strings.Split(string(infoResponseBulk), "\r\n") {
			if strings.HasPrefix(line, "redis_version:") {
				result.Version = strings.SplitN(line, ":", 2)[1]
				version_found = true
			} else if strings.HasPrefix(line, "os:") {
				result.OS = strings.SplitN(line, ":", 2)[1]
				os_found = true
			}
			if version_found && os_found {
				break
			}
		}
	}
	bogusResponse, err := scan.SendCommand(scanner.commandMappings["NONEXISTENT"].(string))
	if err != nil {
		return zgrab2.TryGetScanStatus(err), result, err
	}
	result.NonexistentResponse = forceToString(bogusResponse)
	for i := range scanner.customCommands {
		full_cmd := strings.Fields(scanner.customCommands[i])
		resp, err := scan.SendCommand(full_cmd[0], full_cmd[1:]...)
		if err != nil {
			return zgrab2.TryGetScanStatus(err), result, err
		}
		customResponse := CustomResponse{
			Command:   full_cmd[0],
			Arguments: strings.Join(full_cmd[1:], " "),
			Response:  forceToString(resp),
		}
		result.CustomResponses = append(result.CustomResponses, customResponse)
	}
	quitResponse, err := scan.SendCommand(scanner.commandMappings["QUIT"].(string))
	if err == io.EOF && quitResponse == nil {
		quitResponse = NullValue
	} else if err != nil {
		return zgrab2.TryGetScanStatus(err), result, err
	}
	result.QuitResponse = forceToString(quitResponse)
	return zgrab2.SCAN_SUCCESS, &result, nil
}
