import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, type ProxyRow, type TurnStateAccount, type TurnStateExternalConfig, type TurnStateLog } from '../api'
import { useDataLoader } from '../hooks/useDataLoader'
import { useToast } from '../hooks/useToast'
import { getErrorMessage } from '../utils/error'
import PageHeader from '../components/PageHeader'
import StateShell from '../components/StateShell'
import { ProxyField } from '../components/ProxyField'
import { Button } from '@/components/ui/button'
import { Loader2, Zap, Save, Wand2, ScrollText, X } from 'lucide-react'

const MODELS = ['gpt-5.6-terra', 'gpt-5.6-sol', 'gpt-6-astra'] as const

type SourceMode = 'builtin' | 'external'
// 取件来源偏好（UI 选择）持久化到 localStorage；外部接口地址/密钥改存服务器端。
const LS_MODE = 'turn_state_source_mode'

export default function TurnState() {
  const { t } = useTranslation()
  const { showToast } = useToast()

  const loadTurnStates = useCallback(() => api.listTurnStates(), [])
  const { data, loading, error, reload } = useDataLoader<{ accounts: TurnStateAccount[]; external_config: TurnStateExternalConfig } | null>({
    initialData: null,
    load: loadTurnStates,
  })

  // 代理池：给每个账号的代理字段做下拉数据源。
  const [proxyPool, setProxyPool] = useState<ProxyRow[]>([])
  useEffect(() => {
    let cancelled = false
    void api.listProxies().then((res) => { if (!cancelled) setProxyPool(res.proxies ?? []) }).catch(() => {})
    return () => { cancelled = true }
  }, [])

  // 取件来源模式（builtin/external）——UI 偏好，本地保存。
  const [mode, setMode] = useState<SourceMode>(() => (localStorage.getItem(LS_MODE) as SourceMode) || 'builtin')
  useEffect(() => { localStorage.setItem(LS_MODE, mode) }, [mode])

  // 外部接口配置（服务器端）：URL 从服务器读，token 只知道是否已设置。
  const [externalUrl, setExternalUrl] = useState('')
  const [externalToken, setExternalToken] = useState('')
  const [tokenSet, setTokenSet] = useState(false)
  const [savingConfig, setSavingConfig] = useState(false)
  useEffect(() => {
    if (data?.external_config) {
      setExternalUrl(data.external_config.url || '')
      setTokenSet(Boolean(data.external_config.token_set))
    }
  }, [data?.external_config])

  const handleSaveConfig = useCallback(async () => {
    setSavingConfig(true)
    try {
      // token 留空表示不改动（保留服务器已存的）；填了才覆盖。
      const payload: { external_url?: string; external_token?: string } = { external_url: externalUrl.trim() }
      if (externalToken.trim()) payload.external_token = externalToken.trim()
      const res = await api.saveTurnStateConfig(payload)
      setTokenSet(res.token_set)
      setExternalToken('')
      showToast(t('turnState.configSaved'), 'success')
    } catch (err) {
      showToast(getErrorMessage(err), 'error')
    } finally {
      setSavingConfig(false)
    }
  }, [externalUrl, externalToken, showToast, t])

  // 日志面板
  const [logsOpen, setLogsOpen] = useState(false)

  const accounts = data?.accounts ?? []

  return (
    <StateShell loading={loading} error={error} onRetry={reload} isEmpty={!loading && accounts.length === 0}>
      <PageHeader
        title={t('turnState.title')}
        description={t('turnState.description')}
        onRefresh={reload}
        actions={
          <Button variant="outline" size="sm" onClick={() => setLogsOpen(true)}>
            <ScrollText className="size-3.5" />
            {t('turnState.viewLogs')}
          </Button>
        }
      />

      {/* 取件来源选择 + 外部接口配置 */}
      <div className="mb-4 rounded-xl border border-border bg-card p-4 shadow-sm">
        <div className="mb-3 flex flex-wrap items-center gap-2">
          <span className="text-sm font-semibold text-foreground">{t('turnState.sourceLabel')}</span>
          <div className="flex gap-1 rounded-lg border border-border bg-muted/30 p-0.5">
            {(['builtin', 'external'] as const).map((m) => (
              <button
                key={m}
                type="button"
                onClick={() => setMode(m)}
                className={`rounded-md px-3 py-1 text-xs font-medium transition-colors ${
                  mode === m ? 'bg-primary text-primary-foreground' : 'text-muted-foreground hover:text-foreground'
                }`}
              >
                {m === 'builtin' ? t('turnState.sourceBuiltin') : t('turnState.sourceExternal')}
              </button>
            ))}
          </div>
        </div>
        <p className="mb-3 text-xs text-muted-foreground leading-relaxed">
          {mode === 'external' ? t('turnState.sourceExternalHint') : t('turnState.sourceBuiltinHint')}
        </p>
        {mode === 'external' && (
          <div className="grid gap-2 sm:grid-cols-[1fr_1fr_auto] sm:items-end">
            <div>
              <label className="mb-1 block text-xs font-medium text-muted-foreground">{t('turnState.externalUrlLabel')}</label>
              <input
                type="text"
                value={externalUrl}
                onChange={(e) => setExternalUrl(e.target.value)}
                placeholder="https://xxxx.ngrok-free.app"
                className="w-full rounded-lg border border-border bg-background px-3 py-1.5 text-sm text-foreground placeholder:text-muted-foreground/60 focus:border-primary focus:outline-none focus:ring-1 focus:ring-primary"
              />
            </div>
            <div>
              <label className="mb-1 block text-xs font-medium text-muted-foreground">{t('turnState.externalTokenLabel')}</label>
              <input
                type="password"
                value={externalToken}
                onChange={(e) => setExternalToken(e.target.value)}
                placeholder={tokenSet ? t('turnState.tokenSetPlaceholder') : 'X-Auth-Token'}
                className="w-full rounded-lg border border-border bg-background px-3 py-1.5 text-sm text-foreground placeholder:text-muted-foreground/60 focus:border-primary focus:outline-none focus:ring-1 focus:ring-primary"
              />
            </div>
            <Button variant="default" size="sm" disabled={savingConfig} onClick={handleSaveConfig}>
              {savingConfig ? <Loader2 className="size-3.5 animate-spin" /> : <Save className="size-3.5" />}
              {t('turnState.saveConfig')}
            </Button>
          </div>
        )}
      </div>

      {accounts.length > 0 && (
        <div className="space-y-4">
          {accounts.map((acc) => (
            <AccountCard key={acc.id} account={acc} proxies={proxyPool} mode={mode} onUpdate={reload} />
          ))}
        </div>
      )}

      {logsOpen && <LogsPanel onClose={() => setLogsOpen(false)} />}
    </StateShell>
  )
}

function AccountCard({ account, proxies, mode, onUpdate }: { account: TurnStateAccount; proxies: ProxyRow[]; mode: SourceMode; onUpdate: () => void }) {
  const { t } = useTranslation()
  const { showToast } = useToast()
  const [states, setStates] = useState<Record<string, string>>(() => {
    const init: Record<string, string> = {}
    for (const m of MODELS) init[m] = account.turn_states[m]?.value ?? ''
    return init
  })
  const [generating, setGenerating] = useState<Record<string, boolean>>({})
  const [bulkGenerating, setBulkGenerating] = useState(false)
  const [saving, setSaving] = useState(false)
  const [proxyUrl, setProxyUrl] = useState(account.proxy_url || '')
  const isExternal = mode === 'external'

  // 每账号定时配置
  const [schedEnabled, setSchedEnabled] = useState(account.schedule?.enabled ?? false)
  const [schedInterval, setSchedInterval] = useState(account.schedule?.interval_minutes || 50)
  const [schedSource, setSchedSource] = useState(account.schedule?.source || 'builtin')
  const [savingSched, setSavingSched] = useState(false)

  const generateOne = useCallback(async (model: string): Promise<boolean> => {
    try {
      const res = await api.generateTurnState(account.id, {
        model,
        source: isExternal ? 'external' : 'builtin',
        proxy_url: isExternal ? undefined : (proxyUrl || undefined),
      })
      setStates((prev) => ({ ...prev, [model]: res.turn_state }))
      return true
    } catch (err) {
      showToast(t('turnState.generateFailed', { error: getErrorMessage(err) }), 'error')
      return false
    }
  }, [account.id, isExternal, proxyUrl, showToast, t])

  const handleGenerate = useCallback(async (model: string) => {
    setGenerating((prev) => ({ ...prev, [model]: true }))
    const ok = await generateOne(model)
    if (ok) showToast(t('turnState.generated'), 'success')
    setGenerating((prev) => ({ ...prev, [model]: false }))
  }, [generateOne, showToast, t])

  const handleBulkGenerate = useCallback(async () => {
    setBulkGenerating(true)
    let okCount = 0
    for (const model of MODELS) {
      setGenerating((prev) => ({ ...prev, [model]: true }))
      const ok = await generateOne(model)
      if (ok) okCount++
      setGenerating((prev) => ({ ...prev, [model]: false }))
    }
    setBulkGenerating(false)
    showToast(t('turnState.bulkDone', { ok: okCount, total: MODELS.length }), okCount === MODELS.length ? 'success' : 'error')
  }, [generateOne, showToast, t])

  const handleSave = useCallback(async () => {
    setSaving(true)
    try {
      await api.saveTurnState(account.id, states)
      showToast(t('turnState.saved'), 'success')
      onUpdate()
    } catch (err) {
      showToast(getErrorMessage(err), 'error')
    } finally {
      setSaving(false)
    }
  }, [account.id, states, showToast, t, onUpdate])

  const handleSaveSchedule = useCallback(async () => {
    setSavingSched(true)
    try {
      await api.saveTurnStateSchedule(account.id, {
        enabled: schedEnabled,
        interval_minutes: schedInterval,
        source: schedSource,
      })
      showToast(t('turnState.scheduleSaved'), 'success')
    } catch (err) {
      showToast(getErrorMessage(err), 'error')
    } finally {
      setSavingSched(false)
    }
  }, [account.id, schedEnabled, schedInterval, schedSource, showToast, t])

  return (
    <div className="rounded-xl border border-border bg-card p-4 shadow-sm">
      <div className="mb-3 flex flex-wrap items-center gap-3">
        <div className="flex items-center gap-2">
          <span className={`size-2 rounded-full ${account.disabled ? 'bg-red-500' : 'bg-emerald-500'}`} />
          <span className="text-sm font-semibold text-foreground">{account.email || `ID: ${account.id}`}</span>
        </div>
        <span className="rounded-md border border-border bg-muted/50 px-2 py-0.5 text-xs font-medium text-muted-foreground">
          {account.plan_type || 'unknown'}
        </span>
        <span className="text-xs text-muted-foreground">ID: {account.id}</span>
        <div className="ml-auto">
          <Button variant="default" size="sm" disabled={bulkGenerating} onClick={handleBulkGenerate}>
            {bulkGenerating ? <Loader2 className="size-3.5 animate-spin" /> : <Wand2 className="size-3.5" />}
            {t('turnState.bulkGenerate')}
          </Button>
        </div>
      </div>

      {/* Proxy（外部接口模式下代理由外部服务负责，隐藏） */}
      {!isExternal && (
        <div className="mb-3">
          <ProxyField value={proxyUrl} onChange={setProxyUrl} proxies={proxies} label={t('turnState.proxyLabel')} />
        </div>
      )}

      {/* 每模型 state */}
      <div className="space-y-2">
        {MODELS.map((model) => {
          const stateValue = states[model] || ''
          const isGenerating = generating[model] || false
          return (
            <div key={model} className="flex flex-col gap-1 rounded-lg border border-border/60 bg-muted/20 p-2.5">
              <div className="flex items-center justify-between gap-2">
                <span className="text-xs font-semibold text-foreground">{model}</span>
                <span className="text-[11px] text-muted-foreground">
                  {t('turnState.lengthLabel', { length: stateValue.length })}
                  {stateValue.length > 0 && stateValue.length !== 292 && <span className="ml-1 text-amber-500">⚠</span>}
                </span>
              </div>
              <div className="flex items-center gap-2">
                <input
                  type="text"
                  value={stateValue}
                  onChange={(e) => setStates((prev) => ({ ...prev, [model]: e.target.value }))}
                  placeholder="—"
                  className="min-w-0 flex-1 rounded-md border border-border bg-background px-2.5 py-1.5 font-mono text-xs text-foreground placeholder:text-muted-foreground/40 focus:border-primary focus:outline-none focus:ring-1 focus:ring-primary"
                />
                <Button variant="outline" size="sm" disabled={isGenerating || bulkGenerating} onClick={() => handleGenerate(model)} className="shrink-0">
                  {isGenerating ? <Loader2 className="size-3.5 animate-spin" /> : <Zap className="size-3.5" />}
                  {t('turnState.generate')}
                </Button>
              </div>
            </div>
          )
        })}
      </div>

      {/* 定时生成 + 保存 */}
      <div className="mt-3 flex flex-wrap items-center gap-2 border-t border-border/60 pt-3">
        <label className="flex items-center gap-1.5 text-xs font-medium text-foreground">
          <input type="checkbox" checked={schedEnabled} onChange={(e) => setSchedEnabled(e.target.checked)} className="size-3.5 accent-primary" />
          {t('turnState.scheduleEnable')}
        </label>
        <div className="flex items-center gap-1 text-xs text-muted-foreground">
          <span>{t('turnState.scheduleEvery')}</span>
          <input
            type="number"
            min={1}
            value={schedInterval}
            onChange={(e) => setSchedInterval(Math.max(1, Number(e.target.value) || 1))}
            className="w-16 rounded-md border border-border bg-background px-2 py-1 text-center text-xs text-foreground focus:border-primary focus:outline-none focus:ring-1 focus:ring-primary"
          />
          <span>{t('turnState.scheduleMinutes')}</span>
        </div>
        <select
          value={schedSource}
          onChange={(e) => setSchedSource(e.target.value)}
          className="rounded-md border border-border bg-background px-2 py-1 text-xs text-foreground focus:border-primary focus:outline-none focus:ring-1 focus:ring-primary"
        >
          <option value="builtin">{t('turnState.sourceBuiltin')}</option>
          <option value="external">{t('turnState.sourceExternal')}</option>
        </select>
        <Button variant="outline" size="sm" disabled={savingSched} onClick={handleSaveSchedule}>
          {savingSched ? <Loader2 className="size-3.5 animate-spin" /> : null}
          {t('turnState.scheduleSave')}
        </Button>
        <div className="ml-auto">
          <Button variant="default" size="sm" disabled={saving} onClick={handleSave}>
            {saving ? <Loader2 className="size-3.5 animate-spin" /> : <Save className="size-3.5" />}
            {t('turnState.save')}
          </Button>
        </div>
      </div>
    </div>
  )
}

function LogsPanel({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation()
  const [logs, setLogs] = useState<TurnStateLog[]>([])
  const timerRef = useRef<number | null>(null)

  const refresh = useCallback(() => {
    void api.getTurnStateLogs().then((res) => setLogs(res.logs ?? [])).catch(() => {})
  }, [])

  useEffect(() => {
    refresh()
    timerRef.current = window.setInterval(refresh, 3000)
    return () => { if (timerRef.current) window.clearInterval(timerRef.current) }
  }, [refresh])

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4" onClick={onClose}>
      <div className="flex max-h-[80vh] w-full max-w-3xl flex-col rounded-xl border border-border bg-card shadow-2xl" onClick={(e) => e.stopPropagation()}>
        <div className="flex items-center justify-between border-b border-border px-4 py-3">
          <div className="flex items-center gap-2 text-sm font-semibold text-foreground">
            <ScrollText className="size-4" />
            {t('turnState.logsTitle')}
          </div>
          <button onClick={onClose} className="text-muted-foreground hover:text-foreground"><X className="size-4" /></button>
        </div>
        <div className="min-h-0 flex-1 overflow-auto p-2">
          {logs.length === 0 ? (
            <div className="p-8 text-center text-sm text-muted-foreground">{t('turnState.logsEmpty')}</div>
          ) : (
            <table className="w-full text-left text-xs">
              <thead className="text-muted-foreground">
                <tr className="border-b border-border">
                  <th className="px-2 py-1.5 font-medium">{t('turnState.logTime')}</th>
                  <th className="px-2 py-1.5 font-medium">{t('turnState.logAccount')}</th>
                  <th className="px-2 py-1.5 font-medium">{t('turnState.logModel')}</th>
                  <th className="px-2 py-1.5 font-medium">{t('turnState.logSource')}</th>
                  <th className="px-2 py-1.5 font-medium">{t('turnState.logResult')}</th>
                </tr>
              </thead>
              <tbody>
                {logs.map((l, i) => (
                  <tr key={i} className="border-b border-border/40">
                    <td className="whitespace-nowrap px-2 py-1.5 font-mono text-[11px] text-muted-foreground">{l.time?.replace('T', ' ').replace(/[+Z].*$/, '')}</td>
                    <td className="px-2 py-1.5">{l.email || l.account_id}</td>
                    <td className="px-2 py-1.5 font-mono text-[11px]">{l.model}</td>
                    <td className="px-2 py-1.5">{l.source === 'external' ? t('turnState.sourceExternal') : t('turnState.sourceBuiltin')}</td>
                    <td className="px-2 py-1.5">
                      {l.ok ? (
                        <span className="text-emerald-500">✓ {l.length}{l.node ? ` · ${l.node}` : ''}</span>
                      ) : (
                        <span className="text-red-500" title={l.error}>✗ {l.error}</span>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      </div>
    </div>
  )
}
