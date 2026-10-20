package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/robfig/cron/v3"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	log.SetFlags(log.LstdFlags)
	log.SetPrefix("[seltool] ")
	if err := run(); err != nil {
		log.Printf("启动失败: %v", err)
		os.Exit(1)
	}
}
func run() error {
	configPath := flag.String("config", "./config/custom.yml", "配置文件路径")
	once := flag.Bool("once", false, "只执行一次任务后退出")
	noSchedule := flag.Bool("no-schedule", false, "不注册定时任务")
	flag.Parse()
	cfg, err := loadConfig(*configPath)
	if err != nil {
		fallback := defaultConfig()
		applyEnvOverrides(fallback)
		notifyStartup(NewWebhook(fallback.Automation.StartupNotification).WithWeCom(fallback.Automation.Notify.WeCom), fallback.Automation.StartupNotification, NewAlertState(), err)
		return err
	}
	alerts := NewAlertState()
	webhook := NewWebhook(cfg.Automation.StartupNotification).WithWeCom(cfg.Automation.Notify.WeCom)
	notifyStore := NewNotifyStore("./config/notify-settings.json")
	if saved, ok, err := notifyStore.Load(); err != nil {
		return err
	} else if ok {
		webhook.SetWeCom(saved.toConfig())
		cfg.Automation.Notify.OnlyOnFailure = saved.OnlyOnFailure
		log.Printf("已加载 Web 面板保存的通知设置")
	}
	fail := func(err error) error {
		notifyStartup(webhook, cfg.Automation.StartupNotification, alerts, err)
		return err
	}
	if cfg.Automation.Browser.DriverPath != "" {
		log.Printf("browser.driver-path在CDP版本无需使用；已忽略，不会下载chromedriver")
	}
	accounts := NewAccountStore(cfg.Automation.UserAccountFile)
	if err := accounts.Load(); err != nil {
		return fail(err)
	}
	templates := NewTemplateStore(cfg.Automation.TemplatesFile)
	if err := templates.Load(); err != nil {
		return fail(err)
	}
	targets := NewTargetStore(cfg.Automation.TargetsFile)
	targets.Configure(cfg.Automation.Targets, templates)
	if err := targets.Load(); err != nil {
		return fail(err)
	}
	history := NewHistory(cfg.Automation.TargetHistoryFile)
	history.Load()
	settings := NewSettingsStore("./config/cookiecloud-settings.json")
	if saved, ok, err := settings.Load(); err != nil {
		return fail(err)
	} else if ok {
		cfg.Automation.CookieCloud.URL = saved.URL
		cfg.Automation.CookieCloud.Key = saved.Key
		cfg.Automation.CookieCloud.Password = saved.Password
		log.Printf("已加载 Web 面板保存的 CookieCloud 设置")
	}
	cookies := NewCookieCloud(cfg.Automation.CookieCloud, alerts)
	automation := NewAutomation(cfg, targets, history, cookies, webhook, alerts)
	defer automation.Shutdown()
	if *once {
		if len(targets.Get()) == 0 {
			log.Printf("没有配置任何目标，退出")
			return nil
		}
		if !automation.ExecuteOnce("命令行执行") {
			return fmt.Errorf("任务未能启动")
		}
		return nil
	}
	ptSiteTable = NewPTSiteTable("./config/pt-sites.json")
	ptIconTable = NewPTIconTable("./config/pt-icons.json", builtinPTIconsJSON)
	mpSyncStore = NewMoviePilotSyncStore("./config/moviepilot-sync.json")
	autoImportStore := NewAutoImportStore("./config/auto-import.json")
	aiCfg, aiOK, aiErr := autoImportStore.Load()
	if aiErr != nil {
		return fail(aiErr)
	}
	if aiOK {
		log.Printf("自动导入新站点: %v", aiCfg.Enabled)
	}
	mpCfg, mpOK, mpErr := mpSyncStore.Load()
	if mpErr != nil {
		return fail(mpErr)
	}
	if mpOK {
		log.Printf("已加载 MoviePilot 同步设置: %s (自动同步=%v)", mpCfg.URL, mpCfg.Enabled)
	}
	loadSyncRecord()
	scheduleStore := NewScheduleStore("./config/schedule-settings.json")
	var scheduler *Scheduler
	if !*noSchedule {
		scheduler = NewScheduler(func() { automation.ExecuteOnce("定时执行") })
		set := ScheduleSettings{Enabled: true, Cron: cfg.Automation.Schedule.Cron, Zone: cfg.Automation.Schedule.Zone}
		if saved, ok, e := scheduleStore.Load(); e != nil {
			return fail(e)
		} else if ok {
			set = saved
		}
		if e := scheduler.Apply(set); e != nil {
			return fail(e)
		}
	}
	// 每日同步 CookieCloud：默认 04:00，可用 SELTOOL_COOKIECLOUD_SYNC_CRON 覆盖，设为 off 关闭。
	syncCron := strings.TrimSpace(os.Getenv("SELTOOL_COOKIECLOUD_SYNC_CRON"))
	if syncCron == "" {
		syncCron = "0 0 4 * * *"
	}
	var syncScheduler *cron.Cron
	if !*noSchedule && strings.ToLower(syncCron) != "off" && cookies.Config().Enabled() {
		loc, e := time.LoadLocation(cfg.Automation.Schedule.Zone)
		if e != nil {
			loc = time.Local
		}
		syncScheduler = cron.New(cron.WithSeconds(), cron.WithLocation(loc))
		if _, e := syncScheduler.AddFunc(syncCron, func() {
			data, err := cookies.FetchAllCookies()
			if err != nil {
				log.Printf("CookieCloud 每日同步失败: %v", err)
				return
			}
			syncCookiesOK(data)
			total := 0
			for _, list := range data {
				total += len(list)
			}
			log.Printf("CookieCloud 每日同步完成：%d 个域名 / %d 条 Cookie", len(data), total)
			if aiCfg.Enabled {
				res, e := RunAutoImport(data, targets)
				if e != nil {
					log.Printf("自动导入新站点失败: %v", e)
				} else if len(res.Added) > 0 {
					log.Printf("自动导入 %d 个新站点: %s", len(res.Added), res.Summary())
					if aiCfg.Notify {
						webhook.Send("CookieCloud 自动导入", fmt.Sprintf("从 CookieCloud 发现 %d 个新站点，已自动加入目标列表：\n%s",
							len(res.Added), res.Summary()))
					}
				}
			}
		}); e != nil {
			log.Printf("CookieCloud 同步任务表达式无效，已跳过: %v", e)
			syncScheduler = nil
		}
	}
	if scheduler != nil {
		scheduler.SetMoviePilotJob(func(c MoviePilotSyncConfig) {
			n, e := SyncFromMoviePilot(c, ptSiteTable)
			if e != nil {
				log.Printf("MoviePilot 自动同步失败: %v", e)
				return
			}
			log.Printf("MoviePilot 自动同步完成：%d 个站点", n)
		})
		if mpOK {
			scheduler.SetMoviePilot(mpCfg)
		}
	}
	server := NewServer(cfg, accounts, NewSessionStore(), targets, history, templates, automation, alerts, settings, cookies, notifyStore, webhook, scheduleStore, scheduler, ptSiteTable, ptIconTable, mpSyncStore, autoImportStore)
	httpServer := &http.Server{
		Addr:              cfg.Automation.Listen,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// 请求体读取与响应写出都要有上限，否则慢速连接可以长期占用 goroutine。
		ReadTimeout:  2 * time.Minute,
		WriteTimeout: 2 * time.Minute,
		IdleTimeout:  90 * time.Second,
	}
	// Bind synchronously before publishing success; a busy port is startup failure.
	listener, e := net.Listen("tcp", cfg.Automation.Listen)
	if e != nil {
		return fail(fmt.Errorf("HTTP监听失败: %w", e))
	}
	defer listener.Close()
	errs := make(chan error, 1)
	go func() { defer recoverAndLog("HTTP 服务"); errs <- httpServer.Serve(listener) }()
	log.Printf("Web 控制台监听 %s", cfg.Automation.Listen)
	if scheduler != nil {
		defer scheduler.Stop()
		snap := scheduler.Snapshot()
		if snap.Enabled {
			log.Printf("定时任务已注册: %s (%s)", snap.Cron, snap.Zone)
		} else {
			log.Printf("定时任务未启用")
		}
	}
	if syncScheduler != nil {
		syncScheduler.Start()
		defer syncScheduler.Stop()
		log.Printf("CookieCloud 每日同步已注册: %s", syncCron)
	}
	startupDone := make(chan struct{})
	go func() {
		defer recoverAndLog("启动任务")
		defer close(startupDone)
		var startupErr error
		if cfg.Automation.RunOnStartup {
			automation.ExecuteOnce("启动执行")
			startupErr = automation.TaskError()
		}
		notifyStartup(webhook, cfg.Automation.StartupNotification, alerts, startupErr)
	}()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(stop)
	select {
	case <-stop:
	case e := <-errs:
		if e != nil && e != http.ErrServerClosed {
			return fail(e)
		}
	}
	log.Printf("正在退出…")
	if scheduler != nil {
		scheduler.Stop()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(ctx)
	return nil
}
