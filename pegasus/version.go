package pegasus

import (
	"github.com/HearthSim/hs-proto-go/pegasus/util"
	"github.com/golang/protobuf/proto"
)

type Version struct{}

func (v *Version) Init(sess *Session) {
	sess.RegisterPacket(util.GetAssetsVersion_ID, OnAssetsVersion)
}

func OnAssetsVersion(s *Session, body []byte) *Packet {
	res := util.AssetsVersionResponse{}
	res.Version = proto.Int32(9786)
	return EncodePacket(util.AssetsVersionResponse_ID, &res)
}
