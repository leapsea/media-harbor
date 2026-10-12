import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import ts from '../node_modules/typescript/lib/typescript.js'
import sfc from '../node_modules/@vue/compiler-sfc/dist/compiler-sfc.cjs.js'

// 所有页面共用词库，检查文案覆盖和命名参数，避免切换后遗漏中文或破坏插值。
const root = new URL('../src/', import.meta.url)
const dictionaries = ['en', 'hi', 'ru', 'ja'].map(code => JSON.parse(fs.readFileSync(new URL(`i18n/${code}.json`, root), 'utf8')))
const parameters = value => [...value.matchAll(/\{(\w+)\}/g)].map(match => match[1]).sort()
test('五种界面语言的文案覆盖与插值参数一致', () => {
 const keys = Object.keys(dictionaries[0]).sort()
 for (const dictionary of dictionaries) {
  assert.deepEqual(Object.keys(dictionary).sort(), keys)
  for (const key of keys) {
   assert.ok(dictionary[key].trim(), key)
   assert.deepEqual(parameters(dictionary[key]), parameters(key), key)
  }
 }
 const components = fs.readdirSync(new URL('components/', root)).filter(name => name.endsWith('.vue')).map(name => `components/${name}`)
 for (const name of ['App.vue', ...components, 'stores/media.ts']) {
  const source = fs.readFileSync(new URL(name, root), 'utf8')
  const script = name.endsWith('.vue') ? sfc.parse(source).descriptor.scriptSetup.content : source
  const ast = ts.createSourceFile(name, script, ts.ScriptTarget.Latest, true)
  function walk(node) {
   if (ts.isStringLiteral(node) && /[\u4e00-\u9fff]/.test(node.text)) {
    assert.ok(keys.includes(node.text), `${name}: ${node.text}`)
   }
   ts.forEachChild(node, walk)
  }
  walk(ast)
  for (const match of source.matchAll(/t\((['"])([^'"\n]+)\1/g)) {
   if (/[\u4e00-\u9fff]/.test(match[2])) assert.ok(keys.includes(match[2]), `${name}: ${match[2]}`)
  }
 }
})
