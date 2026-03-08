package clickhousebackup

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"strings"
	"time"

	"github.com/hashicorp/go-multierror"

	"github.com/nixys/nxs-backup/interfaces"
	"github.com/nixys/nxs-backup/misc"
	"github.com/nixys/nxs-backup/modules/backend/exec_cmd"
	"github.com/nixys/nxs-backup/modules/backend/targz"
	"github.com/nixys/nxs-backup/modules/logger"
	"github.com/nixys/nxs-backup/modules/metrics"
)

type job struct {
	name             string
	tmpDir           string
	needToMakeBackup bool
	safetyBackup     bool
	deferredCopying  bool
	diskRateLimit    int64
	storages         interfaces.Storages
	targets          map[string]target
	dumpedObjects    map[string]interfaces.DumpObject
	appMetrics       *metrics.Data
}

type target struct {
	host            string
	port            string
	username        string
	password        string
	dbName          string
	tables          []string
	extraKeys       []string
	useConfig       bool
	configPath      string
	remoteStorage   string
	s3Bucket        string
	s3Path          string
}

type JobParams struct {
	Name             string
	TmpDir           string
	NeedToMakeBackup bool
	SafetyBackup     bool
	DeferredCopying  bool
	DiskRateLimit    int64
	Storages         interfaces.Storages
	Sources          []SourceParams
	Metrics          *metrics.Data
}

type SourceParams struct {
	Name               string
	Host               string
	Port               string
	Username           string
	Password           string
	TargetDBs          []string
	TargetTables       []string
	ExcludeTables      []string
	ExtraKeys          []string
	UseConfig          bool
	ConfigPath         string
	RemoteStorage      string
	S3Bucket           string
	S3Path             string
}

func Init(jp JobParams) (interfaces.Job, error) {

	if _, err := exec_cmd.Exec("clickhouse-backup", "--version"); err != nil {
		return nil, fmt.Errorf("Job `%s` init failed. Can't check `clickhouse-backup` version. Please install `clickhouse-backup`. Error: %s ", jp.Name, err)
	}

	if _, err := exec_cmd.Exec("tar", "--version"); err != nil {
		return nil, fmt.Errorf("Job `%s` init failed. Can't check `tar` version. Please install `tar`. Error: %s ", jp.Name, err)
	}

	j := job{
		name:             jp.Name,
		tmpDir:           jp.TmpDir,
		needToMakeBackup: jp.NeedToMakeBackup,
		safetyBackup:     jp.SafetyBackup,
		deferredCopying:  jp.DeferredCopying,
		diskRateLimit:    jp.DiskRateLimit,
		storages:         jp.Storages,
		targets:          make(map[string]target),
		dumpedObjects:    make(map[string]interfaces.DumpObject),
		appMetrics: jp.Metrics.RegisterJob(
			metrics.JobData{
				JobName:       jp.Name,
				JobType:       misc.ClickHouse,
				TargetMetrics: make(map[string]metrics.TargetData),
			},
		),
	}

	for _, src := range jp.Sources {
		for _, db := range src.TargetDBs {
			ofs := src.Name + "/" + db

			var tables []string
			if len(src.TargetTables) > 0 && !misc.Contains(src.TargetTables, "all") {
				for _, tbl := range src.TargetTables {
					if !misc.Contains(src.ExcludeTables, tbl) {
						tables = append(tables, tbl)
					}
				}
			}

			j.targets[ofs] = target{
				host:          src.Host,
				port:          src.Port,
				username:      src.Username,
				password:      src.Password,
				dbName:        db,
				tables:        tables,
				extraKeys:     src.ExtraKeys,
				useConfig:     src.UseConfig,
				configPath:    src.ConfigPath,
				remoteStorage: src.RemoteStorage,
				s3Bucket:      src.S3Bucket,
				s3Path:        src.S3Path,
			}

			j.appMetrics.Job[j.name].TargetMetrics[ofs] = metrics.TargetData{
				Source: src.Name,
				Target: db,
				Values: make(map[string]float64),
			}
		}
	}

	return &j, nil
}

func (j *job) SetOfsMetrics(ofs string, metricsMap map[string]float64) {
	for m, v := range metricsMap {
		j.appMetrics.Job[j.name].TargetMetrics[ofs].Values[m] = v
	}
}

func (j *job) GetName() string {
	return j.name
}

func (j *job) GetTempDir() string {
	return j.tmpDir
}

func (j *job) GetType() misc.BackupType {
	return misc.ClickHouse
}

func (j *job) GetTargetOfsList() (ofsList []string) {
	for ofs := range j.targets {
		ofsList = append(ofsList, ofs)
	}
	return
}

func (j *job) GetStoragesCount() int {
	return len(j.storages)
}

func (j *job) GetDumpObjects() map[string]interfaces.DumpObject {
	return j.dumpedObjects
}

func (j *job) ListBackups() interfaces.JobTargets {
	jt := make(interfaces.JobTargets)

	for tn := range j.targets {
		jt[tn] = make(interfaces.TargetsOnStorages)
		jt[tn] = j.storages.ListBackups(tn)
	}

	return jt
}

func (j *job) SetDumpObjectDelivered(ofs string) {
	dumpObj := j.dumpedObjects[ofs]
	dumpObj.Delivered = true
	j.dumpedObjects[ofs] = dumpObj
}

func (j *job) IsBackupSafety() bool {
	return j.safetyBackup
}

func (j *job) NeedToMakeBackup() bool {
	return j.needToMakeBackup
}

func (j *job) NeedToUpdateIncMeta() bool {
	return false
}

func (j *job) DeleteOldBackups(logCh chan logger.LogRecord, ofsPath string) error {
	logCh <- logger.Log(j.name, "").Debugf("Starting rotate outdated backups.")
	return j.storages.DeleteOldBackups(logCh, j, ofsPath)
}

func (j *job) CleanupTmpData() error {
	return j.storages.CleanupTmpData(j)
}

func (j *job) DoBackup(logCh chan logger.LogRecord, tmpDir string) error {
	var errs *multierror.Error

	for ofsPart, tgt := range j.targets {
		startTime := time.Now()

		j.SetOfsMetrics(ofsPart, map[string]float64{
			metrics.BackupOk:        float64(0),
			metrics.BackupTime:      float64(0),
			metrics.DeliveryOk:      float64(0),
			metrics.DeliveryTime:    float64(0),
			metrics.BackupSize:      float64(0),
			metrics.BackupTimestamp: float64(startTime.Unix()),
		})

		tmpBackupFile := misc.GetFileFullPath(tmpDir, ofsPart, "tar", "", true)

		if err := os.MkdirAll(path.Dir(tmpBackupFile), os.ModePerm); err != nil {
			logCh <- logger.Log(j.name, "").Errorf("Unable to create tmp dir with next error: %s", err)
			errs = multierror.Append(errs, err)
			continue
		}

		if err := j.createTmpBackup(logCh, tmpBackupFile, tgt); err != nil {
			j.SetOfsMetrics(ofsPart, map[string]float64{
				metrics.BackupTime: float64(time.Since(startTime).Nanoseconds() / 1e6),
			})
			logCh <- logger.Log(j.name, "").Errorf("Unable to create temp backups %s: %v", tmpBackupFile, err)
			errs = multierror.Append(errs, err)
			continue
		}

		fileInfo, _ := os.Stat(tmpBackupFile)
		j.SetOfsMetrics(ofsPart, map[string]float64{
			metrics.BackupOk:   float64(1),
			metrics.BackupTime: float64(time.Since(startTime).Nanoseconds() / 1e6),
			metrics.BackupSize: float64(fileInfo.Size()),
		})

		logCh <- logger.Log(j.name, "").Debugf("Created temp backups %s", tmpBackupFile)

		j.dumpedObjects[ofsPart] = interfaces.DumpObject{TmpFile: tmpBackupFile}

		if !j.deferredCopying {
			if err := j.storages.Delivery(logCh, j); err != nil {
				logCh <- logger.Log(j.name, "").Errorf("Failed to delivery backup. Errors: %v", err)
				errs = multierror.Append(errs, err)
			}
		}
	}

	if err := j.storages.Delivery(logCh, j); err != nil {
		logCh <- logger.Log(j.name, "").Errorf("Failed to delivery backup. Errors: %v", err)
		errs = multierror.Append(errs, err)
	}

	return errs.ErrorOrNil()
}

func (j *job) createTmpBackup(logCh chan logger.LogRecord, tmpBackupFile string, tgt target) error {
	backupName := fmt.Sprintf("nxs_%s_%s", tgt.dbName, time.Now().Format("20060102_150405"))
	tmpBackupDir := path.Join(path.Dir(tmpBackupFile), backupName)
	defer func() { _ = os.RemoveAll(tmpBackupDir) }()

	var args []string

	if tgt.useConfig {
		configPath := tgt.configPath
		if configPath == "" {
			configPath = "/etc/clickhouse-backup/config.yml"
		}
		args = append(args, "--config", configPath)
	}

	if tgt.host != "" {
		args = append(args, "-h", tgt.host)
	}
	if tgt.port != "" {
		args = append(args, "--port", tgt.port)
	}
	if tgt.username != "" {
		args = append(args, "-u", tgt.username)
	}
	if tgt.password != "" {
		args = append(args, "-p", tgt.password)
	}

	if len(tgt.tables) > 0 {
		args = append(args, "-t", strings.Join(tgt.tables, ","))
	}

	if len(tgt.extraKeys) > 0 {
		args = append(args, tgt.extraKeys...)
	}

	args = append(args, "create", backupName)

	var stderr, stdout bytes.Buffer
	logCh <- logger.Log(j.name, "").Infof("Starting ClickHouse backup for `%s`", tgt.dbName)

	cmd := exec.Command("clickhouse-backup", args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	logCh <- logger.Log(j.name, "").Debugf("Backup cmd: %s", cmd.String())

	if err := cmd.Run(); err != nil {
		logCh <- logger.Log(j.name, "").Errorf("Unable to backup `%s`. Error: %s", tgt.dbName, err)
		logCh <- logger.Log(j.name, "").Debugf("STDOUT: %s", stdout.String())
		logCh <- logger.Log(j.name, "").Debugf("STDERR: %s", stderr.String())
		return err
	}

	backupSource := fmt.Sprintf("/var/lib/clickhouse/backup/%s", backupName)
	if tgt.remoteStorage != "" {
		backupSource = fmt.Sprintf("/var/lib/clickhouse/backup/%s", backupName)
	}

	if _, err := os.Stat(backupSource); err != nil {
		logCh <- logger.Log(j.name, "").Errorf("Backup source not found: %s", backupSource)
		return err
	}

	if err := targz.Tar(targz.TarOpts{
		Src:         backupSource,
		Dst:         tmpBackupFile,
		Incremental: false,
		Gzip:        true,
		SaveAbsPath: false,
		RateLim:     j.diskRateLimit,
		Excludes:    nil,
	}); err != nil {
		logCh <- logger.Log(j.name, "").Errorf("Unable to make tar: %s", err)
		var serr targz.Error
		if errors.As(err, &serr) {
			logCh <- logger.Log(j.name, "").Debugf("STDERR: %s", serr.Stderr)
		}
		return err
	}

	cmd = exec.Command("clickhouse-backup", "delete", backupName)
	if err := cmd.Run(); err != nil {
		logCh <- logger.Log(j.name, "").Warnf("Failed to delete temporary backup %s: %v", backupName, err)
	}

	logCh <- logger.Log(j.name, "").Infof("Backup of `%s` completed", tgt.dbName)

	return nil
}

func (j *job) Close() error {
	for _, st := range j.storages {
		_ = st.Close()
	}
	return nil
}