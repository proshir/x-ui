package job

import (
	"x-ui/logger"
	"x-ui/web/service"
	"x-ui/database"
	"x-ui/database/model"
    "os"
 	ss "strings"
	"regexp"
    "encoding/json"
	"gorm.io/gorm"
    "strconv"
	"time"

)

type CheckClientIpJob struct {
	xrayService    service.XrayService
	inboundService service.InboundService
}
var job *CheckClientIpJob
  
func NewCheckClientIpJob() *CheckClientIpJob {
	job = new(CheckClientIpJob)
	return job
}

func (j *CheckClientIpJob) Run() {
	logger.Debug("Check Client IP Job...")
	processLogFile()
}

var lastChecked time.Time

func processLogFile() {
	accessLogPath := GetAccessLogPath()
	if(accessLogPath == "") {
		logger.Warning("xray log not init in config.json")
		return
	}

    data, err := os.ReadFile(accessLogPath)
	InboundClientIps := make(map[string][]string)
    checkError(err)

	if (lastChecked.IsZero() || time.Now().Sub(lastChecked) > time.Minute ) {
		if err := os.Truncate(GetAccessLogPath(), 0); err != nil {
			checkError(err)
		}
		lastChecked = time.Now()
	}

	lines := ss.Split(string(data), "\n")
	for _, line := range lines {
		ipRegx, _ := regexp.Compile(`[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+`)
		emailRegx, _ := regexp.Compile(`email:.+`)

		matchesIp := ipRegx.FindString(line)
		if(len(matchesIp) > 0) {
			ip := string(matchesIp)
			if( ip == "127.0.0.1" || ip == "1.1.1.1") {
				continue
			}

			matchesEmail := emailRegx.FindString(line)
			if(matchesEmail == "") {
				continue
			}
			matchesEmail = ss.Split(matchesEmail, "email: ")[1]
	
			if(InboundClientIps[matchesEmail] != nil) {
				if(contains(InboundClientIps[matchesEmail],ip)){
					continue
				}
			}
			InboundClientIps[matchesEmail] = append(InboundClientIps[matchesEmail],ip)
		}
	}
	var inboundsClientIps []*model.InboundClientIps
	var idsDiable []int
	for clientEmail, ips := range InboundClientIps {
		inboundClientIps, idDisable := GetInboundClientIps(clientEmail, ips)
		if inboundClientIps != nil {
			inboundsClientIps = append(inboundsClientIps, inboundClientIps)
		}
		if idDisable != nil {
			idsDiable = append(idsDiable, *idDisable)
		}
	}

	err = CheckBlockInbounds(idsDiable)
	checkError(err)
	err = AddInboundsClientIps(inboundsClientIps)
	checkError(err)
}
func GetAccessLogPath() string {
	
    config, err := os.ReadFile("bin/config.json")
    checkError(err)

	jsonConfig := map[string]interface{}{}
    err = json.Unmarshal([]byte(config), &jsonConfig)
	checkError(err)
	if(jsonConfig["log"] != nil) {
		jsonLog := jsonConfig["log"].(map[string]interface{})
		if(jsonLog["access"] != nil) {

			accessLogPath := jsonLog["access"].(string)

			return accessLogPath
		}
	}
	return ""

}
func checkError(e error) {
    if e != nil {
		logger.Warning("client ip job err:", e)
	}
}
func contains(s []string, str string) bool {
	for _, v := range s {
		if v == str {
			return true
		}
	}

	return false
}

func ClearInboudClientIps(tx *gorm.DB) error {
	err := tx.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&model.InboundClientIps{}).Error
	checkError(err)
	return err
}

func GetInboundClientIps(clientEmail string, ips []string) (*model.InboundClientIps, *int) {
	jsonIps, err := json.Marshal(ips)
	if err != nil {
		return nil, nil
	}

	inboundClientIps := &model.InboundClientIps{}
	inboundClientIps.ClientEmail = clientEmail
	inboundClientIps.Ips = string(jsonIps)

	inbound, err := GetInboundByEmail(clientEmail)
	if err != nil {
		return nil, nil
	}
	limitIpRegx, _ := regexp.Compile(`"limitIp": .+`)
	limitIpMactch := limitIpRegx.FindString(inbound.Settings)
	limitIpMactch =  ss.Split(limitIpMactch, `"limitIp": `)[1]
    limitIp, err := strconv.Atoi(limitIpMactch)
	if err != nil {
		return nil, nil
	}
	if(limitIp < len(ips) && limitIp != 0 && inbound.Enable) {
		return inboundClientIps, &inbound.Id
	}

	return inboundClientIps, nil
}

func AddInboundsClientIps(inboundsClientIps []*model.InboundClientIps) error {
	if inboundsClientIps == nil || len(inboundsClientIps) == 0 {
		return nil
	}
	db := database.GetDB()
	tx := db.Begin()
	err := ClearInboudClientIps(tx)
	if err != nil {
		tx.Rollback()
		return err
	}
	err = tx.Save(inboundsClientIps).Error
	if err != nil {
		tx.Rollback()
		return err
	}
	tx.Commit()
	return nil
}

func GetInboundByEmail(clientEmail string) (*model.Inbound, error) {
	db := database.GetDB()
	var inbounds *model.Inbound
	err := db.Model(model.Inbound{}).Where("settings LIKE ?", "%" + clientEmail + "%").Find(&inbounds).Error
	if err != nil && err != gorm.ErrRecordNotFound {
		return nil, err
	}
	return inbounds, nil
}

func CheckBlockInbounds(ids []int) error {
	db := database.GetDB()
	result := db.Model(model.Inbound{}).
		Where("1 = 1").
		Update("blocked", gorm.Expr("id in ?", ids))
	logger.Debug("Block IDs:", ids)
	err := result.Error

	if err == nil {
		job.xrayService.SetToNeedRestart()
	}

	return err
}
