export interface JsonTables {
  tables: Array<JsonTable>
}
export interface JsonTable {
  tableName: string
  partitionKeyName: string
  sortKeyName?: string
  gsi?: Array<GlobalSecondaryIndex>
}
export interface GlobalSecondaryIndex {
  name: string
  rangeKey: string
  sortKey?: string
}

export const JsonTablesData: JsonTables = {
  tables: [
    {
      tableName: 'users',
      partitionKeyName: 'id',
      gsi: [
        {
          name: 'EmailIndex',
          rangeKey: 'Email',
        },
      ],
    },
    {
      tableName: 'user-logins',
      partitionKeyName: 'provider',
      sortKeyName: 'id',
      gsi: [
        {
          name: 'EmailIndex',
          rangeKey: 'email',
        },
      ],
    },
    {
      tableName: 'households',
      partitionKeyName: 'id',
    },
    {
      tableName: 'members',
      partitionKeyName: 'householdId',
      sortKeyName: 'userId',
      gsi: [
        {
          name: 'UserIdIndex',
          rangeKey: 'userId',
          sortKey: 'householdId',
        },
      ],
    },
    {
      tableName: 'todo-items',
      partitionKeyName: 'id',
      gsi: [
        {
          name: 'OwnerIndex',
          rangeKey: 'OwnerId',
          sortKey: 'id',
        },
      ],
    },
    {
      tableName: 'done-items',
      partitionKeyName: 'id',
      gsi: [
        {
          name: 'OwnerAsofIndex',
          rangeKey: 'Owner',
          sortKey: 'Asof',
        },
      ],
    },
    {
      tableName: 'refresh-tokens',
      partitionKeyName: 'id',
    },
    {
      tableName: 'invites',
      partitionKeyName: 'id',
      gsi: [
        {
          name: 'HouseholdIndex',
          rangeKey: 'HouseholdID',
          sortKey: 'CreatedAt',
        },
      ],
    },
  ],
}