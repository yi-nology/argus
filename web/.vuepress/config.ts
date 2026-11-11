import { defaultTheme } from '@vuepress/theme-default'
import { defineUserConfig } from 'vuepress'
import { viteBundler } from '@vuepress/bundler-vite'

// Argus 宣传页（VuePress 2）。base=/argus/ 对齐 GitHub Pages 项目站路径
// （yi-nology.github.io/argus/）；视觉基调延续原单页（蓝紫 accent+衬线标题）。
export default defineUserConfig({
  lang: 'zh-CN',
  title: 'Argus',
  description: 'PR 自动审查 Bot —— 多 agent 编排，确定性裁决；真实回帖、确定性真值表、影响面视图',
  base: '/argus/',
  bundler: viteBundler(),
  head: [
    ['link', { rel: 'icon', type: 'image/svg+xml', href: 'favicon.svg' }],
    ['meta', { name: 'theme-color', content: '#2563eb' }],
  ],
  theme: defaultTheme({
    navbar: [
      { text: '能力', link: '/' },
      { text: 'README', link: 'https://github.com/yi-nology/argus/blob/main/README.md' },
      { text: 'GitHub', link: 'https://github.com/yi-nology/argus' },
    ],
    repo: 'yi-nology/argus',
    repoLabel: 'GitHub',
    docsDir: 'web',
    editLink: false,
    tip: '提示',
    warning: '注意',
    danger: '警告',
    notFound: ['页面不存在'],
    backToHome: '返回首页',
  }),
})
