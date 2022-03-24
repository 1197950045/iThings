package eventDevSub

import (
	"context"
	"encoding/json"
	"github.com/i-Things/things/shared/def"
	"github.com/i-Things/things/shared/errors"
	"github.com/i-Things/things/shared/utils"
	"github.com/i-Things/things/src/dmsvr/dm"
	"github.com/i-Things/things/src/dmsvr/internal/domain/deviceSend"
	device2 "github.com/i-Things/things/src/dmsvr/internal/domain/deviceTemplate"
	"github.com/i-Things/things/src/dmsvr/internal/repo"
	"github.com/i-Things/things/src/dmsvr/internal/repo/mysql"
	"github.com/i-Things/things/src/dmsvr/internal/svc"
	"github.com/zeromicro/go-zero/core/logx"
	"strings"
	"time"
)

type PublishLogic struct {
	ctx    context.Context
	svcCtx *svc.ServiceContext
	logx.Logger
	ld       *dm.LoginDevice
	pt       *mysql.ProductTemplate
	template *device2.Template
	topics   []string
	dreq     device2.DeviceReq
	dd       repo.DeviceDataRepo
}

func NewPublishLogic(ctx context.Context, svcCtx *svc.ServiceContext) *PublishLogic {
	return &PublishLogic{
		ctx:    ctx,
		svcCtx: svcCtx,
		Logger: logx.WithContext(ctx),
	}
}

func (l *PublishLogic) initMsg(msg *deviceSend.Elements) error {
	var err error
	l.ld, err = dm.GetClientIDInfo(msg.ClientID)
	if err != nil {
		return err
	}
	l.pt, err = l.svcCtx.ProductTemplate.FindOne(l.ld.ProductID)
	if err != nil {
		return err
	}
	l.template, err = device2.NewTemplate([]byte(l.pt.Template))
	if err != nil {
		return err
	}
	err = utils.Unmarshal([]byte(msg.Payload), &l.dreq)
	if err != nil {
		return errors.Parameter.AddDetail("things topic is err:" + msg.Topic)
	}
	l.dd = l.svcCtx.DeviceData(l.ctx)
	l.topics = strings.Split(msg.Topic, "/")
	return nil
}

func (l *PublishLogic) DeviceResp(msg *deviceSend.Elements, err error, data map[string]interface{}) {
	l.svcCtx.DevClient.DeviceResp(l.dreq.Method, l.dreq.ClientToken, l.topics, err, data)
}

func (l *PublishLogic) HandlePropertyReport(msg *deviceSend.Elements) error {
	tp, err := l.template.VerifyReqParam(l.dreq, device2.PROPERTY)
	if err != nil {
		l.DeviceResp(msg, err, nil)
		return err
	} else if len(tp) == 0 {
		err := errors.Parameter.AddDetail("need right param")
		l.DeviceResp(msg, err, nil)
		return err
	}
	params := device2.ToVal(tp)
	timeStamp := l.dreq.GetTimeStamp(time.Unix(msg.Timestamp, 0))
	err = l.dd.InsertPropertiesData(l.ld.ProductID, l.ld.DeviceName, params, timeStamp)
	if err != nil {
		l.DeviceResp(msg, errors.Database, nil)
		l.Errorf("HandlePropertyReport|InsertPropertyData|err=%+v", err)
		return err
	}
	l.DeviceResp(msg, errors.OK, nil)
	return nil
}

func (l *PublishLogic) HandlePropertyGetStatus(msg *deviceSend.Elements) error {
	respData := make(map[string]interface{}, len(l.template.Properties))
	switch l.dreq.Type {
	case device2.REPORT:
		for id, _ := range l.template.Property {
			data, err := l.dd.GetPropertyDataWithID(l.ld.ProductID, l.ld.DeviceName, id, def.PageInfo2{
				TimeStart: 0,
				TimeEnd:   0,
				Limit:     1,
			})
			if err != nil {
				l.Errorf("HandlePropertyGetStatus|GetPropertyDataWithID|get id:%s|err:%s",
					id, err.Error())
				return err
			}
			if len(data) == 0 {
				l.Slowf("HandlePropertyGetStatus|GetPropertyDataWithID|not find id:%s", id)
				continue
			}
			respData[id] = data[0].Param
		}
	default:
		err := errors.Parameter.AddDetailf("not suppot type :%s", l.dreq.Type)
		l.DeviceResp(msg, err, nil)
		return err
	}
	l.DeviceResp(msg, errors.OK, respData)
	return nil
}

func (l *PublishLogic) HandleProperty(msg *deviceSend.Elements) error {
	l.Slowf("PublishLogic|HandleProperty")
	switch l.dreq.Method {
	case device2.REPORT, device2.REPORT_INFO:
		return l.HandlePropertyReport(msg)
	case device2.GET_STATUS:
		return l.HandlePropertyGetStatus(msg)
	case device2.CONTROL_REPLY:
		return l.HandleResp(msg)
	default:
		return errors.Method
	}
	return nil
}

func (l *PublishLogic) HandleEvent(msg *deviceSend.Elements) error {
	l.Slowf("PublishLogic|HandleEvent")
	dbData := repo.Event{}
	dbData.ID = l.dreq.EventID
	dbData.Type = l.dreq.Type
	if l.dreq.Method != device2.EVENT_POST {
		return errors.Method
	}
	tp, err := l.template.VerifyReqParam(l.dreq, device2.EVENT)
	if err != nil {
		l.DeviceResp(msg, err, nil)
		return err
	}
	dbData.Params = device2.ToVal(tp)
	dbData.TimeStamp = l.dreq.GetTimeStamp(time.Unix(msg.Timestamp, 0))

	err = l.dd.InsertEventData(l.ld.ProductID, l.ld.DeviceName, &dbData)
	if err != nil {
		l.DeviceResp(msg, errors.Database, nil)
		l.Errorf("InsertEventData|err=%+v", err)
		return errors.Database.AddDetail(err)
	}
	l.DeviceResp(msg, errors.OK, nil)

	return nil
}
func (l *PublishLogic) HandleResp(msg *deviceSend.Elements) error {
	l.Slowf("PublishLogic|HandleResp")
	resp := device2.DeviceResp{}
	err := json.Unmarshal([]byte(msg.Payload), &resp)
	if err != nil {
		return errors.Parameter.AddDetail(err)
	}
	l.svcCtx.DevClient.DeviceReqSendResp(&resp, msg.Topic)
	return nil
}

func (l *PublishLogic) HandleThing(msg *deviceSend.Elements) error {
	l.Slowf("PublishLogic|HandleThing")
	if len(l.topics) < 5 || l.topics[1] != "up" {
		return errors.Parameter.AddDetail("things topic is err:" + msg.Topic)
	}
	switch l.topics[2] {
	case def.PROPERTY_METHOD: //属性上报
		return l.HandleProperty(msg)
	case def.EVENT_METHOD: //事件上报
		return l.HandleEvent(msg)
	case def.ACTION_METHOD: //设备响应行为执行结果
		return l.HandleResp(msg)
	default:
		return errors.Parameter.AddDetail("things topic is err:" + msg.Topic)
	}
	return nil
}
func (l *PublishLogic) HandleOta(msg *deviceSend.Elements) error {
	l.Slowf("PublishLogic|HandleOta")
	return nil
}

func (l *PublishLogic) HandleDefault(msg *deviceSend.Elements) error {
	l.Slowf("PublishLogic|HandleDefault")
	return nil

}
func (l *PublishLogic) Handle(msg *deviceSend.Elements) (err error) {
	l.Infof("PublishLogic|req=%+v", msg)
	err = l.initMsg(msg)
	if err != nil {
		return err
	}
	if len(l.topics) > 1 {
		switch l.topics[0] {
		case "$thing":
			err = l.HandleThing(msg)
		case "$ota":
			err = l.HandleOta(msg)
		case l.pt.ProductID:
			err = l.HandleDefault(msg)
		default:
			err = errors.Parameter.AddDetailf("not suppot topic :%s", msg.Topic)
		}
	} else {
		err = errors.Parameter.AddDetailf("need topic :%s", msg.Topic)
	}
	l.svcCtx.DeviceLog.Insert(&mysql.DeviceLog{
		ProductID:   l.ld.ProductID,
		Action:      "publish",
		Timestamp:   l.dreq.GetTimeStamp(time.Unix(msg.Timestamp, 0)), // 操作时间
		DeviceName:  l.ld.DeviceName,
		TranceID:    utils.TraceIdFromContext(l.ctx),
		RequestID:   l.dreq.ClientToken,
		Content:     string(msg.Payload),
		Topic:       msg.Topic,
		ResultType:  errors.Fmt(err).GetCode(),
		CreatedTime: time.Now(),
	})
	return err
}
