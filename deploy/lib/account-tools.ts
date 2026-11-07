import * as sts from "@aws-sdk/client-sts"

var __callerIdentity: Promise<sts.GetCallerIdentityResponse> | undefined
export async function lookupAccountId(region?: string): Promise<string> {
    // First check for CDK_DEFAULT_ACCOUNT (set by CDK CLI during synthesis/deploy)
    if (process.env.CDK_DEFAULT_ACCOUNT) {
        return process.env.CDK_DEFAULT_ACCOUNT
    }
    // Check for explicit account ID override
    if (process.env.ACCOUNT_ID) {
        return process.env.ACCOUNT_ID
    }
    try {
        const identity = await callerIdentity(region)
        return identity.Account!!
    } catch (e) {
        // During synthesis without credentials, use a placeholder
        console.warn(`Could not lookup account ID: ${e}. Using placeholder.`)
        return '123456789012'
    }
}

export async function lookupUserId(region?: string): Promise<string> {
    const identity = await callerIdentity(region)
    return identity.UserId!!
}

async function callerIdentity(region?: string): Promise<sts.GetCallerIdentityResponse> {
    if (__callerIdentity === undefined) {
        const client = new sts.STSClient({ region: region || "us-east-1" })
        __callerIdentity = client.send(new sts.GetCallerIdentityCommand({}))
    }
    return __callerIdentity
}