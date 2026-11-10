import 'source-map-support/register'
import * as fs from 'fs'
import * as path from 'path'
import * as cdk from 'aws-cdk-lib'
import { env } from 'process'
import * as iam from 'aws-cdk-lib/aws-iam'
import * as logs from 'aws-cdk-lib/aws-logs'
import * as lambda from 'aws-cdk-lib/aws-lambda'
import * as apigateway from 'aws-cdk-lib/aws-apigateway'
import * as s3 from 'aws-cdk-lib/aws-s3'
import * as s3deploy from 'aws-cdk-lib/aws-s3-deployment'
import * as certificatemanager from 'aws-cdk-lib/aws-certificatemanager'
import * as route53 from 'aws-cdk-lib/aws-route53'
import * as targets from 'aws-cdk-lib/aws-route53-targets'
import * as cloudfront from 'aws-cdk-lib/aws-cloudfront'
import * as origins from 'aws-cdk-lib/aws-cloudfront-origins'
import { HostedZoneInfo, matchingHostedZone } from './lib/domain-tools'
import * as dynamodb from 'aws-cdk-lib/aws-dynamodb'
import * as customResources from 'aws-cdk-lib/custom-resources'
import * as ssm from 'aws-cdk-lib/aws-ssm'
import * as ses from 'aws-cdk-lib/aws-ses'
import * as sesActions from 'aws-cdk-lib/aws-ses-actions'
import * as secretsmanager from 'aws-cdk-lib/aws-secretsmanager'
import { lookupAccountId } from './lib/account-tools'

const accountIdPromise = lookupAccountId()

const emissaryDomain = env.DOMAIN || ""
if (emissaryDomain === "") {
  throw new Error("Please specify a DOMAIN")
}

const awsRegion = env.AWS_REGION || ""
if (awsRegion == "") {
  throw new Error("Please specify an AWS_REGION")
}

// The MAIL FROM subdomain is also the hostname the application is served on.
const mailFromDomain = `mail.${emissaryDomain}`

// Lambda custom runtimes require the handler to be a file named `bootstrap` in
// the deployment package. Build it with `./run.sh build:lambda`.
const appAssetDirectory = path.join(__dirname, '..', 'mailer', 'cmd', 'frontend')
const appBootstrap = path.join(appAssetDirectory, 'bootstrap')

const app = new cdk.App()

// components to deploy
// 

// javascript workaround - need access to async code in the top level function
async function init() {
  const hostedZoneInfo = await matchingHostedZone(emissaryDomain, awsRegion, true)

  emailIdStack(hostedZoneInfo)
  const emailInStackDetails = emailInStack(hostedZoneInfo)
  await emailClientStack(hostedZoneInfo, emailInStackDetails)

  console.log(`Configured email for account id ${await accountIdPromise}`)
}


function emailIdStack(
  hostedZoneInfo: HostedZoneInfo,
) {
  const stack = new cdk.Stack(app, `emissary-id-${emissaryDomain.replaceAll(".", "-")}`, {})

  const identity = new ses.EmailIdentity(stack, `emissary-email-identity`, {
    identity: ses.Identity.domain(emissaryDomain),
    mailFromDomain: mailFromDomain,
  })

  // The Easy DKIM records complete the SES verification of the domain.
  for (let i = 0; i < identity.dkimRecords.length; i++) {
    new route53.CfnRecordSet(stack, `emissary-email-dkim-${i + 1}`, {
      hostedZoneId: hostedZoneInfo.id,
      name: identity.dkimRecords[i].name,
      type: 'CNAME',
      resourceRecords: [identity.dkimRecords[i].value],
      ttl: '1800',
    })
  }


  // MX record for the MAIL FROM domain. 
  new route53.CfnRecordSet(stack, 'emissary-email-mx', {
    hostedZoneId: hostedZoneInfo.id,
    name: mailFromDomain,
    type: 'MX',
    resourceRecords: [`10 feedback-smtp.${awsRegion}.amazonses.com`],
    ttl: '1800',
  })

  // TXT SPF record for the MAIL FROM domain
  new route53.CfnRecordSet(stack, 'emissary-email-spf', {
    hostedZoneId: hostedZoneInfo.id,
    name: mailFromDomain,
    type: 'TXT',
    resourceRecords: [`"v=spf1 include:amazonses.com ~all"`],
    ttl: '1800',
  })

  // DMARC TXT record for email authentication. The rua mailbox lives in the
  // receiving domain (which now has the inbound MX record), so reports are
  // delivered to SES and through the receipt rule below.
  new route53.CfnRecordSet(stack, 'emissary-email-dmarc', {
    hostedZoneId: hostedZoneInfo.id,
    name: `_dmarc.${emissaryDomain}`,
    type: 'TXT',
    resourceRecords: [`"v=DMARC1;p=quarantine;rua=mailto:dmarc@${emissaryDomain}"`],
    ttl: '1800',
  })
}

interface emailInStackDetails {
  stack: cdk.Stack
  bucket: s3.Bucket
  bucketName: string
  bucketNameParam: string
}
function emailInStack(
  hostedZoneInfo: HostedZoneInfo,
): emailInStackDetails {
  const stack = new cdk.Stack(app, `emissary-in-${emissaryDomain.replaceAll(".", "-")}`, {})
  const inboundEndpoint = `inbound-smtp.${awsRegion}.amazonaws.com`
  // MX record for the receiving domain.
  new route53.CfnRecordSet(stack, 'emissary-email-inbound-mx', {
    hostedZoneId: hostedZoneInfo.id,
    name: emissaryDomain,
    type: 'MX',
    resourceRecords: [`10 ${inboundEndpoint}`],
    ttl: '1800',
  })


  // Raw inbound messages (EML, as received) are written to this bucket. The
  // receipt rule's S3 action grants SES PutObject through a bucket policy, so
  // no intermediate topic or consumer Lambda is needed.
  const bucket = new s3.Bucket(stack, `emissary-inbound-email-bucket`, {
    blockPublicAccess: s3.BlockPublicAccess.BLOCK_ALL,
    encryption: s3.BucketEncryption.S3_MANAGED,
    enforceSSL: true,
    autoDeleteObjects: true,
    removalPolicy: cdk.RemovalPolicy.DESTROY,
  })

  // Publish the bucket name so it can be read if required
  // N.B. Need to support multiple emissary instances to this
  // is qualified
  const bucketNameParam = `/emissary/${emissaryDomain.replaceAll(".", "-")}/bucketname`
  new ssm.StringParameter(stack, 'emissary-inbound-email-bucket-parameter', {
    parameterName: bucketNameParam,
    stringValue: bucket.bucketName,
    description: 'S3 bucket that stores raw EML messages received by SES',
  })

  // Create a receipt rule that dumps each inbound message to S3. Objects are
  // written at the bucket root: Emissary derives an email's id from its full
  // object key, so a slash-separated prefix would break opening a message.
  const ruleSet = new ses.ReceiptRuleSet(stack, 'emissary-rule-set', {
    // A fixed name is required so the activation call below is deterministic
    // (and so re-deploys do not pile up rule sets with generated names).
    receiptRuleSetName: `emissary-${emissaryDomain.replaceAll(".", "-")}`,
    rules: [
      {
        recipients: [emissaryDomain],
        actions: [
          new sesActions.S3({
            bucket: bucket,
          }),
        ],
      },
    ],
  })

  // CloudFormation has no resource for the active receipt rule set, and SES
  // only applies rules that belong to the active set: a rule set that is never
  // activated receives nothing at all. Activate it after creation and
  // deactivate it before deletion (CloudFormation cannot delete an active rule
  // set). SetActiveReceiptRuleSet is account-wide per region, so this stack
  // owns the active rule set for its region.
  const activateRuleSet = new customResources.AwsCustomResource(stack, 'emissary-inbound-activate-ruleset', {
    onCreate: {
      service: 'SES',
      action: 'setActiveReceiptRuleSet',
      parameters: { RuleSetName: ruleSet.receiptRuleSetName },
      physicalResourceId: customResources.PhysicalResourceId.of('emissary-inbound-rule-set-active'),
    },
    onUpdate: {
      service: 'SES',
      action: 'setActiveReceiptRuleSet',
      parameters: { RuleSetName: ruleSet.receiptRuleSetName },
      physicalResourceId: customResources.PhysicalResourceId.of('emissary-inbound-rule-set-active'),
    },
    onDelete: {
      service: 'SES',
      action: 'setActiveReceiptRuleSet',
      // No RuleSetName deactivates the active rule set.
      parameters: {},
    },
    // The handler's bundled SDK already includes SES.
    installLatestAwsSdk: false,
    policy: customResources.AwsCustomResourcePolicy.fromStatements([
      new iam.PolicyStatement({
        actions: ['ses:SetActiveReceiptRuleSet'],
        resources: ['*'],
      }),
    ]),
  })
  activateRuleSet.node.addDependency(ruleSet)

  new cdk.CfnOutput(stack, 'output-EmissaryInboundEmailBucket', {
    value: bucket.bucketName,
    description: `S3 bucket for raw EML messages (also at Parameter ${bucketNameParam}`,
    exportName: 'EmissaryInboundEmailBucket',
  })

  return {
    stack,
    bucket,
    bucketName: bucket.bucketName,
    bucketNameParam,
  }
}

// The application stack serves the Emissary frontend and API from the MAIL
// FROM subdomain (mail.<domain>). The Lambda binary is built into
// mailer/cmd/frontend/bootstrap; when it is missing the stack is skipped so
// that `cdk synth`/`cdk deploy --all` still works for the email-only stacks.
async function emailClientStack(
  hostedZoneInfo: HostedZoneInfo,
  emailInStackDetails: emailInStackDetails,
) {
  if (!fs.existsSync(appBootstrap)) {
    console.warn(
      `Skipping the Emissary app stack: ${appBootstrap} does not exist. ` +
      `Run './run.sh build:lambda' to build it before deploying the app.`,
    )
    return
  }
  const inboundEmailBucket = emailInStackDetails.bucket

  const accountId = await accountIdPromise
  const stack = new cdk.Stack(app, `emissary-client-${emissaryDomain.replaceAll(".", "-")}`, {})

  const role = new iam.Role(stack, 'emissary-app-role', {
    assumedBy: new iam.ServicePrincipal('lambda.amazonaws.com'),
    roleName: `emissary-${emissaryDomain.replaceAll(".", "-")}-app`,
    managedPolicies: [
      iam.ManagedPolicy.fromAwsManagedPolicyName('service-role/AWSLambdaBasicExecutionRole'),
    ],
  })

  const logGroup = new logs.LogGroup(stack, 'emissary-app-logs', {
    logGroupName: `/logs/emissary/${emissaryDomain.replaceAll(".", "-")}`,
    retention: logs.RetentionDays.THREE_MONTHS,
    removalPolicy: cdk.RemovalPolicy.DESTROY,
  })
  logGroup.grantWrite(role)

  // Read access to the raw inbound EML messages. This is added as a policy on
  // the Lambda role (rather than a bucket grant) so the bucket stays owned by
  // the email-in stack without creating a cross-stack dependency cycle.
  role.addToPolicy(new iam.PolicyStatement({
    sid: 'InboundEmailRead',
    actions: [
      's3:GetObject',
      's3:ListBucket',
    ],
    resources: [
      inboundEmailBucket.bucketArn,
      inboundEmailBucket.arnForObjects('*'),
    ],
    effect: iam.Effect.ALLOW,
  }))

  // Allow sending mail from the verified domain identity.
  role.addToPolicy(new iam.PolicyStatement({
    sid: 'SesSendEmail',
    actions: [
      'ses:SendEmail',
      'ses:SendRawEmail',
    ],
    resources: [
      `arn:aws:ses:${awsRegion}:${accountId}:identity/*`,
    ],
    effect: iam.Effect.ALLOW,
  }))

  // Email read/flag state must survive cold starts, so store it in DynamoDB.
  const metadataTable = new dynamodb.Table(stack, 'emissary-metadata', {
    tableName: `emissary-${emissaryDomain.replaceAll(".", "-")}-metadata`,
    partitionKey: { name: 'id', type: dynamodb.AttributeType.STRING },
    billingMode: dynamodb.BillingMode.PAY_PER_REQUEST,
    removalPolicy: cdk.RemovalPolicy.DESTROY,
  })
  metadataTable.grantReadWriteData(role)

  const authUsername = env.USERNAME || ''
  const authPassword = env.PASSWORD || ''
  if (authPassword === '') {
    console.warn('PASSWORD is not set; the deployed Lambda will not have a default admin user.')
  }

  // Create a secret in Secrets Manager for the JWT secret.
  // If JWT_SECRET is provided, use it; otherwise generate a new one.
  const jwtSecretValue = env.JWT_SECRET || ''
  const jwtSecret = new secretsmanager.Secret(stack, 'emissary-jwt-secret', {
    secretName: `emissary/${emissaryDomain.replaceAll(".", "-")}/jwt-secret`,
    description: 'JWT signing secret for Emissary email application',
    generateSecretString: jwtSecretValue ? undefined : {
      secretStringTemplate: '{}',
      generateStringKey: 'secret',
      passwordLength: 64,
      excludeCharacters: '/@"\'\\',
    },
    removalPolicy: cdk.RemovalPolicy.DESTROY,
  })
  // If a secret value was provided, put it into the secret
  if (jwtSecretValue) {
    new customResources.AwsCustomResource(stack, 'emissary-jwt-secret-value', {
      onCreate: {
        service: 'SecretsManager',
        action: 'putSecretValue',
        parameters: {
          SecretId: jwtSecret.secretArn,
          SecretString: jwtSecretValue,
        },
        physicalResourceId: customResources.PhysicalResourceId.of('emissary-jwt-secret-value'),
      },
      onUpdate: {
        service: 'SecretsManager',
        action: 'putSecretValue',
        parameters: {
          SecretId: jwtSecret.secretArn,
          SecretString: jwtSecretValue,
        },
        physicalResourceId: customResources.PhysicalResourceId.of('emissary-jwt-secret-value'),
      },
      policy: customResources.AwsCustomResourcePolicy.fromStatements([
        new iam.PolicyStatement({
          actions: ['secretsmanager:PutSecretValue'],
          resources: [jwtSecret.secretArn],
        }),
      ]),
    })
  }

  // Grant the Lambda role permission to read the JWT secret
  jwtSecret.grantRead(role)

  const fn = new lambda.Function(stack, 'emissary-fn', {
    runtime: lambda.Runtime.PROVIDED_AL2023,
    architecture: lambda.Architecture.X86_64,
    functionName: `emissary-${emissaryDomain.replaceAll(".", "-")}`,
    description: 'The Emissary email application',
    code: lambda.Code.fromAsset(appAssetDirectory, { exclude: ['**', '!bootstrap'] }),
    handler: 'bootstrap',
    role,
    logGroup,
    timeout: cdk.Duration.seconds(30),
    memorySize: 512,
    environment: {
      // Configuration consumed by mailer/config (and the current frontend).
      USERNAME: authUsername,
      DOMAIN: emissaryDomain,
      USER_MANAGER: env.USER_MANAGER || 'none',
      STORAGE: 's3',
      S3_BUCKET: inboundEmailBucket.bucketName,
      SENDER: 'aws-ses',
      SES_FROM: env.SES_FROM || `no-reply@${emissaryDomain}`,
      SES_REGION: awsRegion,
      METADATA: 'ddb',
      DDB_TABLE: metadataTable.tableName,
      METADATA_REGION: awsRegion,
      ...(authPassword ? { PASSWORD: authPassword } : {}),
      // Pass the JWT secret ARN - the application will resolve it at runtime
      JWT_SECRET: jwtSecret.secretArn,
      JWT_AUDIENCE: mailFromDomain,
      JWT_ISSUER: `emissary-${emissaryDomain}`,
    },
  })

  const restApi = new apigateway.LambdaRestApi(stack, 'emissary-restapi', {
    restApiName: `emissary-${emissaryDomain.replaceAll(".", "-")}`,
    description: 'Emissary email application',
    endpointTypes: [apigateway.EndpointType.REGIONAL],
    handler: fn,
    minCompressionSize: cdk.Size.bytes(0),
    proxy: true,
  })

  // Serve the application on mail.<domain>. A CNAME cannot be used here
  // because the MAIL FROM MX and SPF TXT records already exist at that name,
  // so use an alias A record instead.
  const zone = route53.PublicHostedZone.fromHostedZoneAttributes(
    stack,
    'emissary-app-zone',
    {
      hostedZoneId: hostedZoneInfo.id,
      zoneName: hostedZoneInfo.name,
    },
  )

  const certificate = new certificatemanager.Certificate(stack, 'emissary-app-cert', {
    domainName: mailFromDomain,
    validation: certificatemanager.CertificateValidation.fromDns(zone),
  })

  const apiDomainName = restApi.addDomainName('emissary-app-domain', {
    domainName: mailFromDomain,
    certificate,
  })

  const recordName = mailFromDomain.substring(0, mailFromDomain.length - (hostedZoneInfo.name.length + 1))
  new route53.ARecord(stack, 'emissary-app-a-record', {
    zone,
    recordName,
    target: route53.RecordTarget.fromAlias(new targets.ApiGatewayDomain(apiDomainName)),
  })

  new cdk.CfnOutput(stack, 'output-EmissaryAppUrl', {
    value: `https://${mailFromDomain}`,
    description: 'Emissary application URL',
    exportName: `EmissaryAppUrl-${emissaryDomain.replaceAll(".", "-")}`,
  })
}

// run with async support
init()