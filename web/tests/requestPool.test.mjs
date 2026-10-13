import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import ts from '../node_modules/typescript/lib/typescript.js'
const source = fs.readFileSync(new URL('../src/utils/requestPool.ts', import.meta.url), 'utf8')
const code = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.ESNext } }).outputText
const { createRequestPool } = await import(`data:text/javascript;base64,${Buffer.from(code).toString('base64')}`)

// 故意悬挂旧页面请求，确认新页面不会排在它后面等待。
test('旧页面慢请求不阻塞新页面，同资源复用请求', async () => {
 const pool = createRequestPool()
 let finishOld
 let calls = 0
 const old = pool.run('overview', () => { calls++; return new Promise(resolve => { finishOld = resolve }) })
 assert.equal(pool.run('overview', () => { throw new Error('重复请求') }), old)
 const current = pool.run('settings', async () => 'ready')
 assert.equal(await current, 'ready')
 assert.equal(calls, 1)
 finishOld('late')
 assert.equal(await old, 'late')
})

// 错误只归属当前资源，后续读取允许重试。
test('失败资源清理请求状态，不影响其他资源或后续重试', async () => {
 const pool = createRequestPool()
 await assert.rejects(pool.run('cookies', async () => { throw new Error('offline') }), /offline/)
 assert.equal(await pool.run('engines', async () => 'available'), 'available')
 assert.equal(await pool.run('cookies', async () => 'retry'), 'retry')
})
