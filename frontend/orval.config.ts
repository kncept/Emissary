import { defineConfig } from 'orval';

export default defineConfig({
  jmap: {
    input: {
      target: '../api/jmap.yaml',
    },
    output: {
      target: 'src/api/generated/jmap.ts',
      client: 'fetch',
      schemas: 'src/api/generated/schemas/jmap',
    },
  },
  auth: {
    input: {
      target: '../api/auth.yaml',
    },
    output: {
      target: 'src/api/generated/auth.ts',
      client: 'fetch',
      schemas: 'src/api/generated/schemas/auth',
    },
  },
  users: {
    input: {
      target: '../api/users.yaml',
    },
    output: {
      target: 'src/api/generated/users.ts',
      client: 'fetch',
      schemas: 'src/api/generated/schemas/users',
    },
  },
  support: {
    input: {
      target: '../api/support.yaml',
    },
    output: {
      target: 'src/api/generated/support.ts',
      client: 'fetch',
      schemas: 'src/api/generated/schemas/support',
    },
  },
});