package testsupport

import (
	"bytes"
	"crypto/fips140"

	"github.com/gosnmp/gosnmp"
)

// The SNMPv3 side of the test agents: one USM user, engine discovery, and
// requests accepted only when they authenticate and decrypt under that
// user's keys. Every byte is decoded and encoded by gosnmp itself, so a
// client is tested against what it really puts on the wire.

const (
	// usmStatsUnknownEngineIDs is the report an agent sends to engine
	// discovery (RFC 3414 §4).
	usmStatsUnknownEngineIDs = ".1.3.6.1.6.3.15.1.1.4.0"

	testEngineBoots = 1
	testEngineTime  = 100
	testMsgMaxSize  = 65507
)

// TestEngineID is the test agents' RFC 3411 engine ID, in the
// enterprise-specific format.
var TestEngineID = string([]byte{0x80, 0x00, 0x1f, 0x88, 0x04, 'n', 'e', 't', 'm', 'o', 'n'})

// SNMPv3User is the one user a test SNMPv3 agent knows.
type SNMPv3User struct {
	Name           string
	AuthProtocol   gosnmp.SnmpV3AuthProtocol
	AuthPassphrase string
	PrivProtocol   gosnmp.SnmpV3PrivProtocol
	PrivPassphrase string
}

// SNMPv3PrivacyRefused says why this process cannot put an SNMPv3 authPriv
// message on the wire, or returns "" when it can.
//
// Every SNMPv3 privacy protocol a FIPS build offers is AES in CFB128 mode
// (RFC 3826 and its 192/256-bit extensions). CFB is not part of the Go
// Cryptographic Module, so under GODEBUG=fips140=only crypto/cipher refuses it
// whatever the key size and whichever auth protocol is in force. A test whose
// point is privacy cannot run there; one that only needs a v3 exchange can run
// at authNoPriv instead.
func SNMPv3PrivacyRefused() string {
	if fips140.Enforced() {
		return "GODEBUG=fips140=only refuses AES-CFB128, the cipher mode of every SNMPv3 privacy protocol (RFC 3826)"
	}
	return ""
}

// USMEngine answers SNMPv3 datagrams for one user.
type USMEngine struct {
	usm *gosnmp.UsmSecurityParameters
	// probe is the user without keys: what reads a discovery probe.
	probe *gosnmp.UsmSecurityParameters
}

// NewUSMEngine derives user's keys for the test engine.
func NewUSMEngine(user SNMPv3User) (*USMEngine, error) {
	privProtocol := user.PrivProtocol
	if privProtocol == 0 {
		privProtocol = gosnmp.NoPriv
	}
	usm := &gosnmp.UsmSecurityParameters{
		UserName:                 user.Name,
		AuthoritativeEngineID:    TestEngineID,
		AuthoritativeEngineBoots: testEngineBoots,
		AuthoritativeEngineTime:  testEngineTime,
		AuthenticationProtocol:   user.AuthProtocol,
		AuthenticationPassphrase: user.AuthPassphrase,
		PrivacyProtocol:          privProtocol,
		PrivacyPassphrase:        user.PrivPassphrase,
	}
	if err := usm.InitSecurityKeys(); err != nil {
		return nil, &USMKeyError{Err: err}
	}
	probe := &gosnmp.UsmSecurityParameters{
		UserName:                 user.Name,
		AuthoritativeEngineID:    TestEngineID,
		AuthoritativeEngineBoots: testEngineBoots,
		AuthoritativeEngineTime:  testEngineTime,
		AuthenticationProtocol:   gosnmp.NoAuth,
		PrivacyProtocol:          gosnmp.NoPriv,
	}
	return &USMEngine{usm: usm, probe: probe}, nil
}

// Answer decodes one SNMPv3 datagram and encodes the reply. An engine
// discovery probe is answered by the engine itself; any other request must
// authenticate (and decrypt, at authPriv) under the user's keys, and is then
// answered with what respond makes of it. A request that does not is
// refused with a *USMRejectedError.
func (e *USMEngine) Answer(datagram []byte, respond func(*gosnmp.SnmpPacket) *gosnmp.SnmpPacket) ([]byte, error) {
	// A discovery probe carries an empty engine ID, and gosnmp localizes its
	// configured keys afresh for every engine ID unlike its own: read under
	// the user's keys, each probe would cost a full password-to-key
	// derivation inside the client's timeout. So a datagram is read without
	// keys first; one that is authenticated fails that read, and is read
	// again, under the keys, below.
	request, err := decodeV3(e.probe, bytes.Clone(datagram))
	if err != nil || request.MsgFlags&gosnmp.AuthNoPriv != 0 {
		if request, err = decodeV3(e.usm, datagram); err != nil {
			return nil, &USMRejectedError{Err: err}
		}
	}

	var response *gosnmp.SnmpPacket
	if request.MsgFlags&gosnmp.AuthNoPriv == 0 {
		response = discoveryReport(request)
	} else {
		response = respond(request)
		response.MsgFlags = request.MsgFlags &^ gosnmp.Reportable
		// The request's parameters carry the user's keys and the salt the
		// client chose; a test agent may answer under the same salt.
		response.SecurityParameters = request.SecurityParameters
	}
	response.Version = gosnmp.Version3
	response.SecurityModel = gosnmp.UserSecurityModel
	response.MsgID = request.MsgID
	response.MsgMaxSize = testMsgMaxSize
	response.ContextEngineID = TestEngineID
	response.ContextName = request.ContextName

	return response.MarshalMsg()
}

// discoveryReport answers an unauthenticated discovery probe with the
// engine's ID, boots and time.
// decodeV3 reads one SNMPv3 datagram as user. UnmarshalTrap is gosnmp's
// receive path for unsolicited v3 messages: it checks the message's
// authentication and decrypts it with user's keys, which is just what an
// agent does with a request.
func decodeV3(user *gosnmp.UsmSecurityParameters, datagram []byte) (*gosnmp.SnmpPacket, error) {
	decoder := &gosnmp.GoSNMP{
		Version:            gosnmp.Version3,
		SecurityModel:      gosnmp.UserSecurityModel,
		SecurityParameters: user.Copy(),
	}
	return decoder.UnmarshalTrap(datagram, true)
}

func discoveryReport(request *gosnmp.SnmpPacket) *gosnmp.SnmpPacket {
	return &gosnmp.SnmpPacket{
		MsgFlags: gosnmp.NoAuthNoPriv,
		SecurityParameters: &gosnmp.UsmSecurityParameters{
			AuthoritativeEngineID:    TestEngineID,
			AuthoritativeEngineBoots: testEngineBoots,
			AuthoritativeEngineTime:  testEngineTime,
		},
		PDUType:   gosnmp.Report,
		RequestID: request.RequestID,
		Variables: []gosnmp.SnmpPDU{{
			Name:  usmStatsUnknownEngineIDs,
			Type:  gosnmp.Counter32,
			Value: uint32(1),
		}},
	}
}
