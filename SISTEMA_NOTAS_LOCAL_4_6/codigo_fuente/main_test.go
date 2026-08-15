package main

import "testing"

func TestPCExactUserExample(t *testing.T) {
	loadEmbeddedCatalogs()
	n := Note{ContenidoHTML: `*SECRETARIA MUNICIPAL DE PROTECCIÓN CIVIL REFORMA, CHIAPAS*
*FECHA:*
14 De Agosto Del 2026
*SOLICITA:*
*ESCUDO PAKAL C5 CALLE 911 REFORMA*
*AMBULANCIA:*
FOMPC021
*JEFE DE SERVICIO:*
José Rodolfo De La Cruz Hernandez
*TIPO DE SERVICIO:*
Urgencia
*CRONOMETRIA*
*Aviso:* 16:26 PM
*Llegada al Lugar:*
16:30 PM
*Atencion:* 16:31 PM
*Termino De Servicio:* 16:55 PM
*DATOS DEL PACIENTE:*
*Nombre:* Dolores Bautista Trejo
*Domicilio:* Col.Cactaceas, Calle: Flor De Lirio
*Edad:* 56 años *FN:* 26/08/1970
*Familiar:* Cecilio Javier Trejo (hijo)
*Ocupación:* Ama De Casa
*Lugar de Ocurrencia y Ubicación del Servicio:*
Terminal Transporte El Diamante, Col. Centro, Calle: Joaquín Miguel Gutiérrez
*CAUSA DE LA EMERGENCIA:*
Clínico
*Origen Probable:*
Crisis cognitiva emocional
Discusión
*ANAMNESIS:*
Se acude a servicio Solicitado por órgano regulador con reporte de una persona que se sentía mal en el lugar se encuentra femenina en posición Fowler
Glasgow 4+5+5=15
*Via Aérea -* Permeable
*-Pulso-* Radial
*SIGNOS VITALES INICIALES :*
*Fr:* 25 X Min.
*Fc:* 93 X Min.
*Sat02:* 96%
*P/A:* 192/90
*Glucosa:*
*-Alergias:* Ninguno
*-Medicamentos:* Forxiga
*-Padecimientos:* Diabetes
*-Último Lunch:* 15:00 PM
*-Evento previos:* Discusión
*Escala del Coma de Glasgow Post TX:* 4+5+6=15
*Pertenencias:* No sé tuvo Contacto
*Traslado:* No Amerita / Se estabiliza en el lugar
*Recibe:*
*Atiende:*
José Rodolfo De La Cruz Hernandez
*Operador:* Amado Jair Padrón.`}
	a := analyzeClosure(n)
	if a.Recommended == nil {
		t.Fatalf("sin recomendacion: %+v", a)
	}
	if a.Recommended.Code != "17" {
		t.Fatalf("esperaba 17, obtuvo %s (%s) reason=%s alts=%+v", a.Recommended.Code, a.Recommended.Name, a.Reason, a.Alternatives)
	}
	if a.Confidence != "ALTA" {
		t.Fatalf("esperaba confianza ALTA, obtuvo %s reason=%s", a.Confidence, a.Reason)
	}
}

func TestPCSiteAndHospitalTransfer(t *testing.T) {
	loadEmbeddedCatalogs()
	n := Note{ContenidoHTML: `SECRETARIA MUNICIPAL DE PROTECCIÓN CIVIL
AMBULANCIA: FOMPC021
JEFE DE SERVICIO: Juan
CRONOMETRIA
DATOS DEL PACIENTE:
ANAMNESIS: Paciente consciente
SIGNOS VITALES INICIALES:
FR: 20
FC: 88
SAT02: 97%
Glasgow 4+5+6=15
TRASLADO: Se traslada al Hospital General
RECIBE: Hospital General`}
	a := analyzeClosure(n)
	if a.Recommended == nil || a.Recommended.Code != "35" {
		t.Fatalf("esperaba 35, obtuvo %+v", a)
	}
}

func TestPCTransferWithoutSiteAssessment(t *testing.T) {
	loadEmbeddedCatalogs()
	n := Note{ContenidoHTML: `SECRETARIA MUNICIPAL DE PROTECCIÓN CIVIL
AMBULANCIA: FOMPC021
JEFE DE SERVICIO: Juan
CRONOMETRIA
DATOS DEL PACIENTE:
TRASLADO: Se traslada al Hospital General
RECIBE: Hospital General`}
	a := analyzeClosure(n)
	if a.Recommended == nil || a.Recommended.Code != "46" {
		t.Fatalf("esperaba 46, obtuvo %+v", a)
	}
}

func assertClosure(t *testing.T, corp, text, want string) {
	t.Helper()
	loadEmbeddedCatalogs()
	a := analyzeClosure(Note{Corporacion: corp, ContenidoHTML: text})
	if a.Recommended == nil {
		t.Fatalf("%s: sin recomendación; perfil=%s confianza=%s motivo=%s alts=%+v", corp, a.ProfileLabel, a.Confidence, a.Reason, a.Alternatives)
	}
	if a.Recommended.Code != want {
		t.Fatalf("%s: esperaba %s, obtuvo %s (%s), perfil=%s motivo=%s", corp, want, a.Recommended.Code, a.Recommended.Name, a.ProfileLabel, a.Reason)
	}
	if a.Confidence != "ALTA" || !a.SafeToAutoClose {
		t.Fatalf("%s/%s: cierre correcto pero no seguro: confianza=%s safe=%v motivo=%s", corp, want, a.Confidence, a.SafeToAutoClose, a.Reason)
	}
}

func TestSecurityCorporationProfiles(t *testing.T) {
	for _, corp := range []string{"SPM", "GEP", "FRIP"} {
		t.Run(corp+"_Flagrancia_MP", func(t *testing.T) {
			assertClosure(t, corp, `RESULTADO FINAL: Al arribar la unidad, el responsable fue detenido en flagrancia en el lugar y puesto a disposición del Ministerio Público.`, "5")
		})
		t.Run(corp+"_SinIndicioDelictivo", func(t *testing.T) {
			assertClosure(t, corp, `NOVEDADES: La unidad llegó al lugar, realizó la verificación y no encontró indicio delictivo relacionado con lo reportado.`, "70")
		})
		t.Run(corp+"_PersonaLocalizada", func(t *testing.T) {
			assertClosure(t, corp, `CIERRE: El reportante informa que la persona desaparecida ya fue localizada y se encuentra con sus familiares.`, "60")
		})
	}
}

func TestTrafficCorporationProfiles(t *testing.T) {
	for _, corp := range []string{"TRVM", "GEVP"} {
		t.Run(corp+"_ArregloParticulares", func(t *testing.T) {
			assertClosure(t, corp, `RESULTADO FINAL: Las partes involucradas llegaron a un arreglo entre particulares por su cuenta; no intervino el agente de tránsito.`, "58")
		})
		t.Run(corp+"_Aseguradoras", func(t *testing.T) {
			assertClosure(t, corp, `RESULTADO FINAL: Arribaron los ajustadores y las aseguradoras se responsabilizan de los daños ocasionados.`, "59")
		})
		t.Run(corp+"_Infraccion", func(t *testing.T) {
			assertClosure(t, corp, `CIERRE: El agente de tránsito elaboró tarjeta de infracción al conductor por la falta cometida.`, "56")
		})
		t.Run(corp+"_VehiculoRemitido", func(t *testing.T) {
			assertClosure(t, corp, `CIERRE: El vehículo involucrado fue remitido al corralón para los trámites correspondientes.`, "42")
		})
	}
}

func TestProtectionCivilOperationalProfile(t *testing.T) {
	assertClosure(t, "PC", `SECRETARIA MUNICIPAL DE PROTECCIÓN CIVIL
RESULTADO FINAL: Personal de Protección Civil sofocó el incendio y las llamas quedaron extinguidas.`, "21")
	assertClosure(t, "PC", `SECRETARIA MUNICIPAL DE PROTECCIÓN CIVIL
RESULTADO FINAL: Se realizó el rescate de una persona que se encontraba atrapada.`, "30")
	assertClosure(t, "PC", `SECRETARIA MUNICIPAL DE PROTECCIÓN CIVIL
RESULTADO FINAL: El incendio de pastizal quedó controlado.`, "76")
}

func TestCrossCorporationMedicalOutcomeRequiresExplicitMedicalContext(t *testing.T) {
	assertClosure(t, "SPM", `RESULTADO FINAL: Arribó Protección Civil en ambulancia; paramédicos realizaron valoración médica y signos vitales en el lugar, posteriormente la persona fue trasladada al Hospital General.`, "35")
}

func TestAmbiguousTextDoesNotBecomeTrustedClosure(t *testing.T) {
	loadEmbeddedCatalogs()
	a := analyzeClosure(Note{Corporacion: "SPM", ContenidoHTML: `NOVEDADES: La unidad acudió al lugar y todo quedó sin novedad.`})
	if a.Recommended != nil || a.SafeToAutoClose {
		t.Fatalf("texto ambiguo no debe producir cierre confiable: %+v", a)
	}
}

func TestSecurityAseguradaTrasladadaASPMDisposicionAdministrativa(t *testing.T) {
	loadEmbeddedCatalogs()
	n := Note{Corporacion: "SPM", ContenidoHTML: `La persona asegurada fue trasladada a las instalaciones de Seguridad Pública Municipal para ser puesta a disposición del área correspondiente y determinar su situación administrativa.`}
	a := analyzeClosure(n)
	if a.Recommended == nil {
		t.Fatalf("esperaba sugerencia 23; perfil=%s confianza=%s motivo=%s alternativas=%+v", a.ProfileLabel, a.Confidence, a.Reason, a.Alternatives)
	}
	if a.Recommended.Code != "23" {
		t.Fatalf("esperaba 23, obtuvo %s (%s), motivo=%s", a.Recommended.Code, a.Recommended.Name, a.Reason)
	}
	if a.Confidence != "ALTA" {
		t.Fatalf("esperaba confianza ALTA para la sugerencia, obtuvo %s", a.Confidence)
	}
	if a.SafeToAutoClose {
		t.Fatalf("sin evidencia explícita de flagrancia/en el lugar no debe auto-cerrar")
	}
}

func TestSecurityAseguradaEnLugarTrasladadaASPMPuedeAutoCerrar(t *testing.T) {
	loadEmbeddedCatalogs()
	n := Note{Corporacion: "SPM", ContenidoHTML: `RESULTADO FINAL: En el lugar de los hechos la persona fue asegurada y trasladada a las instalaciones de Seguridad Pública Municipal para ser puesta a disposición del área correspondiente.`}
	a := analyzeClosure(n)
	if a.Recommended == nil || a.Recommended.Code != "23" {
		t.Fatalf("esperaba 23, obtuvo %+v", a)
	}
	if a.Confidence != "ALTA" || !a.SafeToAutoClose {
		t.Fatalf("con evidencia de lugar y disposición esperaba cierre seguro: confianza=%s safe=%v motivo=%s", a.Confidence, a.SafeToAutoClose, a.Reason)
	}
}

func TestSecurityAseguradoMPWithoutFlagrancySuggests5ButNoAutoClose(t *testing.T) {
	loadEmbeddedCatalogs()
	n := Note{Corporacion: "GEP", ContenidoHTML: `La persona fue asegurada y trasladada a la Fiscalía para ser puesta a disposición del Ministerio Público.`}
	a := analyzeClosure(n)
	if a.Recommended == nil || a.Recommended.Code != "5" {
		t.Fatalf("esperaba 5, obtuvo %+v", a)
	}
	if a.SafeToAutoClose {
		t.Fatalf("sin flagrancia/en el lugar no debe auto-cerrar")
	}
}

func TestSemanticDefinitionsVehicleSynonyms(t *testing.T) {
	loadEmbeddedCatalogs()
	cases := []struct {
		corp string
		text string
		want string
	}{
		{"TRVM", `CIERRE: Derivado del percance, el carro fue llevado al encierro vehicular para los trámites correspondientes.`, "42"},
		{"TRVM", `RESULTADO: El conductor huyó del lugar dejando la camioneta; posteriormente la unidad fue ingresada a la pensión oficial.`, "54"},
		{"TRVM", `RESULTADO: El chofer fue asegurado por los agentes y la moto involucrada fue enviada al corralón.`, "55"},
		{"GEVP", `CIERRE: El automóvil quedó bajo resguardo y fue depositado en la pensión oficial después del choque.`, "42"},
	}
	for _, tc := range cases {
		a := analyzeClosure(Note{Corporacion: tc.corp, ContenidoHTML: tc.text})
		if a.Recommended == nil || a.Recommended.Code != tc.want {
			t.Fatalf("%s: esperaba %s, obtuvo %+v", tc.text, tc.want, a)
		}
	}
}

func TestSemanticDefinitionsDispositionSynonyms(t *testing.T) {
	loadEmbeddedCatalogs()
	cases := []struct {
		corp string
		text string
		want string
	}{
		{"SPM", `El individuo fue capturado por los elementos y fue presentado ante la Fiscalía para quedar a disposición del Ministerio Público.`, "5"},
		{"SPM", `El masculino fue arrestado y trasladado a la comandancia municipal para quedar a disposición del juez calificador.`, "23"},
	}
	for _, tc := range cases {
		a := analyzeClosure(Note{Corporacion: tc.corp, ContenidoHTML: tc.text})
		if a.Recommended == nil || a.Recommended.Code != tc.want {
			t.Fatalf("%s: esperaba %s, obtuvo %+v", tc.text, tc.want, a)
		}
		if a.SafeToAutoClose {
			t.Fatalf("sin flagrancia explícita no debe auto-cerrar: %+v", a)
		}
	}
}

func TestAll65DefinitionScenariosFlexibleWording(t *testing.T) {
	loadEmbeddedCatalogs()
	cases := []struct{ corp, text, want string }{
		{"SPM", `RESULTADO: La unidad llegó, verificó el sitio y no se observó lo reportado; no se corroboró la emergencia.`, "2"},
		{"SPM", `CIERRE: Los elementos auxiliaron al ciudadano y mediaron entre las partes para resolver la situación.`, "4"},
		{"GEP", `RESULTADO: El individuo fue capturado por los agentes y entregado a la Fiscalía para quedar a disposición del Ministerio Público.`, "5"},
		{"SPM", `CIERRE: Únicamente se le dio una advertencia verbal al responsable y se retiró sin novedad.`, "6"},
		{"SPM", `NOVEDAD: Se mantuvo presencia policial preventiva y vigilancia en el punto para evitar riesgos.`, "7"},
		{"SPM", `RESULTADO: La referencia proporcionada era incorrecta y la calle indicada no existe.`, "8"},
		{"GEP", `CIERRE: Se dio aviso a la corporación correspondiente, que quedó enterada para seguimiento.`, "13"},
		{"SPM", `RESULTADO: El responsable escapó del lugar y no fue alcanzado por los elementos.`, "15"},
		{"SPM", `RESULTADO: Durante la huida el responsable perdió la vida.`, "16"},
		{"PC", `RESULTADO: Paramédicos tomaron signos vitales y realizaron valoración en el lugar; no requiere traslado.`, "17"},
		{"PC", `RESULTADO: La persona afectada recibió atención en el Hospital General.`, "18"},
		{"PC", `RESULTADO: La persona perdió la vida en el lugar de la emergencia.`, "19"},
		{"PC", `RESULTADO: El escape de gas fue controlado y se cerró la fuga sin riesgo posterior.`, "20"},
		{"PC", `RESULTADO: Personal de bomberos apagó el incendio y las llamas quedaron extinguidas.`, "21"},
		{"SPM", `RESULTADO: Se trabajó en coordinación con Protección Civil y Tránsito en un operativo conjunto.`, "22"},
		{"SPM", `RESULTADO: El individuo fue capturado y entregado al juez calificador para quedar bajo custodia municipal.`, "23"},
		{"PC", `SERVICIO: Se brindó apoyo durante un ejercicio de simulacro y prueba preventiva.`, "24"},
		{"PC", `RESULTADO: El paciente no permitió la valoración y rechazó la atención médica.`, "25"},
		{"PC", `RESULTADO: La persona fue trasladada a un refugio temporal para su resguardo.`, "26"},
		{"PC", `RESULTADO: El occiso fue trasladado al servicio forense para los trámites correspondientes.`, "27"},
		{"SPM", `RESULTADO: La unidad no arribó al lugar y no se tuvo comunicación con la corporación.`, "28"},
		{"SPM", `RESULTADO: Se le brindaron recomendaciones y se le indicó acudir a la Fiscalía para continuar el trámite.`, "29"},
		{"PC", `RESULTADO: Se puso a salvo a la persona que se encontraba atrapada y fue liberada del sitio.`, "30"},
		{"SPM", `RESULTADO: El reportante solicita que no acuda la unidad porque ya no requiere el apoyo.`, "32"},
		{"PC", `RESULTADO: La familia realizará el traslado de la persona por su cuenta en vehículo particular.`, "34"},
		{"PC", `RESULTADO: Paramédicos realizaron valoración y toma de signos en la escena; posteriormente fue trasladado al Hospital General.`, "35"},
		{"SPM", `RESULTADO: No se cuenta con unidad disponible, por lo que no fue posible acudir al servicio.`, "36"},
		{"PC", `RESULTADO: La persona fue trasladada al Hospital General y posteriormente falleció en el lugar.`, "37"},
		{"SPM", `CIERRE: El incidente permanece activo y continúa en monitoreo para seguimiento.`, "38"},
		{"TRVM", `RESULTADO: El agente arribó y las partes firmaron un convenio para resolver el percance.`, "39"},
		{"PC", `RESULTADO: Se evacuó el inmueble y se retiró a las personas del área de riesgo.`, "40"},
		{"SPM", `RESULTADO: El automóvil con reporte de robo fue encontrado y recuperado en otra colonia.`, "41"},
		{"TRVM", `RESULTADO: Tras el choque la camioneta fue remolcada y quedó depositada en la pensión vehicular.`, "42"},
		{"SPM", `RESULTADO: La unidad llegó pero el reportante no salió y no fue posible establecer contacto con él.`, "43"},
		{"PC", `RESULTADO: El paramédico brindó indicaciones de primeros auxilios por teléfono mientras llegaba el apoyo.`, "44"},
		{"GEP", `RESULTADO: Se dio cumplimiento a una orden judicial de aprehensión emitida por la Fiscalía.`, "45"},
		{"PC", `RESULTADO: La persona lesionada fue trasladada al Hospital Regional para su atención.`, "46"},
		{"PC", `RESULTADO: Durante el traslado al hospital la persona perdió la vida.`, "47"},
		{"PC", `RESULTADO: La persona falleció dentro del Hospital General después de su ingreso.`, "48"},
		{"SPM", `RESULTADO: El responsable escapó, posteriormente fue capturado y entregado al juez calificador para quedar a disposición municipal.`, "49"},
		{"SPM", `RESULTADO: El reporte se turnó a la dependencia competente para que continúe con la atención.`, "52"},
		{"SPM", `RESULTADO: Fiscalía notificó que se ejecutaría una diligencia judicial de cateo en el inmueble.`, "53"},
		{"TRVM", `RESULTADO: El conductor escapó dejando el auto; la grúa lo remolcó y quedó en el corralón.`, "54"},
		{"TRVM", `RESULTADO: El conductor fue arrestado y la motocicleta involucrada fue remolcada al depósito vehicular.`, "55"},
		{"TRVM", `RESULTADO: El agente levantó una boleta de infracción al conductor por la falta cometida.`, "56"},
		{"TRVM", `RESULTADO: Después del percance cada conductor asumirá sus propios daños.`, "57"},
		{"TRVM", `RESULTADO: Las partes se arreglaron por su cuenta y no requirieron intervención del agente de tránsito.`, "58"},
		{"TRVM", `RESULTADO: Arribó el ajustador y la compañía de seguros cubrirá los daños del accidente.`, "59"},
		{"SPM", `RESULTADO: La persona que estaba extraviada ya fue ubicada y se encuentra con su familia.`, "60"},
		{"SPM", `RESULTADO: La alarma de un comercio fue reportada por la central privada de seguridad contratada.`, "63"},
		{"SPM", `RESULTADO: Se recibió alarma de una sucursal bancaria mediante llamada de un operador de la central.`, "64"},
		{"SPM", `RESULTADO: La alarma del banco ingresó mediante audio grabado del conmutador automático.`, "65"},
		{"SPM", `RESULTADO: La alarma bancaria fue reportada directamente por SEPROBAN mediante un audio grabado.`, "66"},
		{"SPM", `RESULTADO: La alarma bancaria provino directamente de SEPROBAN mediante llamada de su operador.`, "67"},
		{"SPM", `RESULTADO: La alarma bancaria llegó directamente de SEPROBAN a través de su aplicativo.`, "68"},
		{"SPM", `RESULTADO: La unidad arribó y no ubicó a la parte afectada u ofendida en el sitio.`, "69"},
		{"SPM", `RESULTADO: La unidad llegó, inspeccionó el lugar y no encontró evidencia delictiva de lo reportado.`, "70"},
		{"SPM", `RESULTADO: La unidad no arribó porque se quedó sin combustible durante el recorrido.`, "71"},
		{"SPM", `RESULTADO: Las unidades permanecieron en base por orden del mando superior.`, "73"},
		{"SPM", `RESULTADO: El responsable escapó, luego fue capturado y presentado ante Fiscalía para quedar a disposición del Ministerio Público.`, "74"},
		{"PC", `RESULTADO: El incendio fue apagado por los vecinos y quedó controlado antes del arribo de la unidad.`, "75"},
		{"PC", `RESULTADO: El fuego en la maleza del terreno quedó controlado y sin propagación.`, "76"},
		{"PC", `RESULTADO: El incendio reportado resultó ser una quema de desechos y quedó controlado.`, "77"},
		{"PC", `RESULTADO: Se delimitó el área con cinta de precaución para proteger a peatones y evitar riesgos.`, "78"},
		{"SPM", `RESULTADO: Se realizaron investigaciones y seguimiento de actos delictivos para recabar información.`, "79"},
	}
	if len(cases) != 65 {
		t.Fatalf("la matriz debe cubrir 65 códigos, contiene %d", len(cases))
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			a := analyzeClosure(Note{Corporacion: tc.corp, ContenidoHTML: tc.text})
			if a.Recommended == nil || a.Recommended.Code != tc.want {
				got := "SIN"
				if a.Recommended != nil {
					got = a.Recommended.Code + " " + a.Recommended.Name
				}
				t.Fatalf("esperaba %s, obtuvo %s; confianza=%s motivo=%s alternativas=%+v texto=%s", tc.want, got, a.Confidence, a.Reason, a.Alternatives, tc.text)
			}
		})
	}
}
