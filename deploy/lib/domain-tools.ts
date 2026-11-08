import { Route53Client, ListHostedZonesCommand, ListHostedZonesRequest } from "@aws-sdk/client-route-53"

// const domainNameEndingsToScan = [
//     '.com',
//     '.com.au',
//     '.org.au',
//     '.co.nz',
//     '.co.uk',
// ]

export type HostedZoneInfo = {
    id: string,
    name: string,
}

export async function matchingHostedZone(fqdn: string, region: string, matchEntireFqdn?: boolean): Promise<HostedZoneInfo> {
    const hostedZones = await listDomainNames(region)
    hostedZones.sort((a: HostedZoneInfo, b: HostedZoneInfo): number => {
        return b.name.length - a.name.length
    })

    for (let i = 0; i < hostedZones.length; i++) {
        const hostedZone = hostedZones[i]
        if (fqdn.endsWith(`.${hostedZone.name}`)) {
            return hostedZone
        }
    }
    if (matchEntireFqdn) {
        for (let i = 0; i < hostedZones.length; i++) {
            const hostedZone = hostedZones[i]
            if (fqdn ===`${hostedZone.name}`) {
                return hostedZone
            }
        }
    }
    throw new Error(`No matching hosted zone for ${fqdn}`)
}

export async function listDomainNames(region: string): Promise<Array<HostedZoneInfo>> {
    const client = new Route53Client({
        region: region
    })
    const input: ListHostedZonesRequest = {}
    const command = new ListHostedZonesCommand(input)
    const response = await client.send(command)
    if (!response.HostedZones) return []
    return response.HostedZones.map(hz => {
        let id = hz.Id!
        if (id.startsWith('/hostedzone/')) id = id.substring('/hostedzone/'.length)
        let name = hz.Name!
        if (name.endsWith('.')) name = name.substring(0, name.length - 1)
        return { id, name } as HostedZoneInfo
    })
}

// export function extractHostedZoneFromHostname(fqdn: string) : string {
//     if (fqdn === undefined || fqdn === null || fqdn.trim() === '') throw new Error('Bad fqdn: ' + fqdn)
//     for(let i = 0; i < domainNameEndingsToScan.length; i++) {
//         const knownTldEnding = domainNameEndingsToScan[i]
//         if (fqdn.endsWith(knownTldEnding)) {
//             const hostname = fqdn.substring(0, fqdn.length - knownTldEnding.length)
//             if (hostname.lastIndexOf('.') != -1) {
//                 return fqdn.substring(hostname.lastIndexOf('.') + 1)
//             } else {
//                 return fqdn
//             }
//         }
//     }
//     throw new Error('Unable to determine hosted zone name')
// }

// export async function matchHostedZoneToFQDN(fqdn: string) : Promise<HostedZoneInfo | undefined> {
//     const existingDomainNames = await listDomainNames()
//     const tld = extractHostedZoneFromHostname(fqdn)
//     for(let i = 0; i < domainNameEndingsToScan.length; i++) {
//         let existingDomainName = existingDomainNames[i].name
//         if (existingDomainName.endsWith('.')) existingDomainName = existingDomainName.substring(0, existingDomainName.length - 1)
//         if (existingDomainName === tld) return existingDomainNames[i]
//     }
//     return undefined
// }